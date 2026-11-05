package types

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Validate performs a basic validation of the coin metadata fields. It checks:
//   - Name and Symbol are not blank
//   - Base and Display denominations are valid coin denominations
//   - Base and Display denominations are present in the DenomUnit slice
//   - Base denomination has exponent 0
//   - Denomination units are sorted in ascending order
//   - Denomination units not duplicated
func (m Metadata) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name field cannot be blank")
	}

	if strings.TrimSpace(m.Symbol) == "" {
		return errors.New("symbol field cannot be blank")
	}

	if err := sdk.ValidateDenom(m.Base); err != nil {
		return fmt.Errorf("invalid metadata base denom: %w", err)
	}

	if err := sdk.ValidateDenom(m.Display); err != nil {
		return fmt.Errorf("invalid metadata display denom: %w", err)
	}

	var (
		hasDisplay      bool
		currentExponent uint32 // check that the exponents are increasing
	)

	seenUnits := make(map[string]bool)

	for i, denomUnit := range m.DenomUnits {
		// The first denomination unit MUST be the base
		if i == 0 {
			// validate denomination and exponent
			if denomUnit.Denom != m.Base {
				return fmt.Errorf("metadata's first denomination unit must be the one with base denom '%s'", m.Base)
			}
			if denomUnit.Exponent != 0 {
				return fmt.Errorf("the exponent for base denomination unit %s must be 0", m.Base)
			}
		} else if currentExponent >= denomUnit.Exponent {
			return errors.New("denom units should be sorted asc by exponent")
		}

		currentExponent = denomUnit.Exponent

		if seenUnits[denomUnit.Denom] {
			return fmt.Errorf("duplicate denomination unit %s", denomUnit.Denom)
		}

		if denomUnit.Denom == m.Display {
			hasDisplay = true
		}

		if err := denomUnit.Validate(); err != nil {
			return err
		}

		seenUnits[denomUnit.Denom] = true
	}

	if !hasDisplay {
		return fmt.Errorf("metadata must contain a denomination unit with display denom '%s'", m.Display)
	}

	return nil
}

// Validate performs a basic validation of the denomination unit fields
func (du DenomUnit) Validate() error {
	if err := sdk.ValidateDenom(du.Denom); err != nil {
		return fmt.Errorf("invalid denom unit: %w", err)
	}

	seenAliases := make(map[string]bool)
	for _, alias := range du.Aliases {
		if seenAliases[alias] {
			return fmt.Errorf("duplicate denomination unit alias %s", alias)
		}

		if strings.TrimSpace(alias) == "" {
			return fmt.Errorf("alias for denom unit %s cannot be blank", du.Denom)
		}

		seenAliases[alias] = true
	}

	return nil
}

// isIBCTransferMetadata reports whether m is exactly the metadata that the
// ibc-go transfer module writes for a received voucher: a single exponent 0
// unit holding the base denom of the trace, the full trace path as display,
// and "ibc/" followed by the upper-case hex SHA-256 of that path as base.
// Validate rejects it because its first unit is not the base, yet every chain
// that received an IBC token holds it in state, so an exported genesis must
// still pass genesis validation. It is used only by genesis validation.
func isIBCTransferMetadata(m Metadata) bool {
	if len(m.DenomUnits) != 1 || m.DenomUnits[0] == nil {
		return false
	}
	unit := m.DenomUnits[0]
	// The base denom can be empty: ibc-go parses a received "foo/channel-1"
	// entirely as trace path.
	if unit.Exponent != 0 || len(unit.Aliases) != 0 {
		return false
	}
	if m.URI != "" || m.URIHash != "" {
		return false
	}

	path, ok := strings.CutSuffix(m.Display, "/"+unit.Denom)
	if !ok || !isIBCTracePath(path) {
		return false
	}
	// ibc-go folds a leading port/channel pair of the remainder into the
	// path, so such a unit is not what it would write for this display.
	if segments := strings.SplitN(unit.Denom, "/", 3); len(segments) >= 2 && isIBCChannelID(segments[1]) {
		return false
	}

	hash := sha256.Sum256([]byte(m.Display))
	return m.Base == "ibc/"+strings.ToUpper(hex.EncodeToString(hash[:])) &&
		m.Description == "IBC token from "+m.Display &&
		m.Name == m.Display+" IBC token" &&
		m.Symbol == strings.ToUpper(unit.Denom)
}

// isIBCTracePath reports whether path is a nonempty list of port/channel
// pairs, as in "transfer/channel-0/transfer/channel-12", with identifiers that
// ibc-go accepts in a received packet.
func isIBCTracePath(path string) bool {
	segments := strings.Split(path, "/")
	if len(segments) < 2 || len(segments)%2 != 0 {
		return false
	}
	for i := 0; i < len(segments); i += 2 {
		if !isIBCPortID(segments[i]) || !isIBCChannelID(segments[i+1]) {
			return false
		}
	}
	return true
}

// isIBCPortID reports whether id is a valid IBC port identifier: 2 to 128
// characters out of a-z, A-Z, 0-9 and ._+-#[]<>.
func isIBCPortID(id string) bool {
	if len(id) < 2 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("._+-#[]<>", c):
		default:
			return false
		}
	}
	return true
}

// isIBCChannelID reports whether id is a "channel-<uint64>" IBC channel
// identifier, as ibc-go parses it.
func isIBCChannelID(id string) bool {
	n, ok := strings.CutPrefix(id, "channel-")
	if !ok || len(n) > 20 {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 64)
	return err == nil
}
