package unknownproto

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	protov2 "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// fieldRules is what RejectAmbiguousEncoding needs to know about one field.
type fieldRules struct {
	repeats  bool  // unpacked repeated or map: may occur several times in a row
	packed   bool  // packed repeated scalar
	oneof    int32 // index of a real (non-synthetic) oneof, or -1
	decimal  bool  // gogoproto customtype math.Int or math.LegacyDec
	required bool  // non-nullable singular decimal
}

type messageRules struct {
	fields   map[int32]fieldRules
	required int
}

func newMessageRules(md *descriptorpb.DescriptorProto, proto3 bool) *messageRules {
	r := &messageRules{fields: make(map[int32]fieldRules, len(md.Field))}
	for _, fd := range md.Field {
		repeated := fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		packed := repeated && isScalar(fd) && proto3
		if opts := fd.GetOptions(); opts != nil && opts.Packed != nil {
			packed = repeated && isScalar(fd) && *opts.Packed
		}
		f := fieldRules{repeats: repeated && !packed, packed: packed, oneof: -1}
		if fd.OneofIndex != nil && !fd.GetProto3Optional() {
			f.oneof = fd.GetOneofIndex()
		}
		customType, nullable := gogoOptions(fd.GetOptions())
		f.decimal = strings.HasSuffix(customType, "math.Int") || strings.HasSuffix(customType, "math.LegacyDec")
		f.required = f.decimal && !nullable && !repeated
		if f.required {
			r.required++
		}
		r.fields[fd.GetNumber()] = f
	}
	return r
}

// strictState tracks the fields read so far in one message.
type strictState struct {
	prev     protowire.Number
	oneofs   uint64
	required int
}

// checkStrictField rejects what decoders read differently: fields out of order
// or repeated when singular, two oneof members, invalid UTF-8 and non-decimal
// math.Int/LegacyDec text. checkCanonicalForm adds the gogoproto byte rules.
func (s *strictState) checkStrictField(r *messageRules, fd *descriptorpb.FieldDescriptorProto, num protowire.Number, typ protowire.Type, tagLen int, b []byte) error {
	f := r.fields[int32(num)]
	if num < s.prev || (num == s.prev && !f.repeats) {
		return fmt.Errorf("field %d is out of order or repeated", num)
	}
	if f.oneof >= 0 && num != s.prev {
		if f.oneof >= 64 || s.oneofs&(1<<f.oneof) != 0 {
			return fmt.Errorf("field %d sets a oneof twice", num)
		}
		s.oneofs |= 1 << f.oneof
	}
	if f.required {
		s.required++
	}
	s.prev = num
	if typ == protowire.BytesType {
		v, _ := protowire.ConsumeBytes(b)
		if fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING && !utf8.Valid(v) {
			return fmt.Errorf("field %d is not valid UTF-8", num)
		}
		if f.decimal && !canonicalDecimal(v) {
			return fmt.Errorf("field %d is not a canonical decimal", num)
		}
	}
	return checkCanonicalForm(f, fd, num, typ, tagLen, b)
}

// checkCanonicalForm rejects forms every decoder reads alike but gogoproto never
// writes: long varints, out-of-range ints and bools, and the wrong packing.
func checkCanonicalForm(f fieldRules, fd *descriptorpb.FieldDescriptorProto, num protowire.Number, typ protowire.Type, tagLen int, b []byte) error {
	if tagLen != protowire.SizeTag(num) {
		return fmt.Errorf("field %d tag is not minimal", num)
	}
	if isScalar(fd) && f.packed != (typ == protowire.BytesType) {
		return fmt.Errorf("field %d has the wrong packing", num)
	}
	switch typ {
	case protowire.VarintType:
		return checkVarints(b, fd.GetType(), false)
	case protowire.BytesType:
		v, n := protowire.ConsumeBytes(b)
		if n-len(v) != protowire.SizeVarint(uint64(len(v))) {
			return fmt.Errorf("field %d length prefix is not minimal", num)
		}
		if f.packed {
			return checkVarints(v, fd.GetType(), true)
		}
	}
	return nil
}

// checkVarints checks one varint, or all elements of a packed field.
func checkVarints(b []byte, t descriptorpb.FieldDescriptorProto_Type, packed bool) error {
	switch toProtowireType(t) {
	case protowire.Fixed32Type:
		if len(b)%4 != 0 {
			return errors.New("packed fixed32 length")
		}
		return nil
	case protowire.Fixed64Type:
		if len(b)%8 != 0 {
			return errors.New("packed fixed64 length")
		}
		return nil
	}
	for len(b) > 0 {
		v, n := protowire.ConsumeVarint(b)
		if n < 0 || n != protowire.SizeVarint(v) {
			return errors.New("varint is not minimal")
		}
		switch t {
		case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
			if v > 1 {
				return errors.New("bool out of range")
			}
		case descriptorpb.FieldDescriptorProto_TYPE_INT32, descriptorpb.FieldDescriptorProto_TYPE_ENUM:
			if int64(v) != int64(int32(v)) {
				return errors.New("int32 out of range")
			}
		case descriptorpb.FieldDescriptorProto_TYPE_UINT32, descriptorpb.FieldDescriptorProto_TYPE_SINT32:
			if v > 0xFFFF_FFFF {
				return errors.New("uint32 out of range")
			}
		}
		if !packed {
			return nil
		}
		b = b[n:]
	}
	return nil
}

// canonicalDecimal reports whether v is an integer as math.Int and LegacyDec
// write it: optional minus, digits, no leading zeros, not "-0".
func canonicalDecimal(v []byte) bool {
	if len(v) > 0 && v[0] == '-' {
		v = v[1:]
		if len(v) == 1 && v[0] == '0' {
			return false
		}
	}
	if len(v) == 0 || (v[0] == '0' && len(v) > 1) {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// gogoOptions returns the gogoproto customtype (65003) and nullable (65001)
// options, which descriptorpb keeps as raw extension bytes.
func gogoOptions(opts *descriptorpb.FieldOptions) (customType string, nullable bool) {
	nullable = true
	b, err := protov2.Marshal(opts)
	if err != nil {
		return "", true
	}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch {
		case num == 65003 && typ == protowire.BytesType:
			v, _ := protowire.ConsumeBytes(b)
			customType = string(v)
		case num == 65001 && typ == protowire.VarintType:
			v, _ := protowire.ConsumeVarint(b)
			nullable = v != 0
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			break
		}
		b = b[m:]
	}
	return customType, nullable
}
