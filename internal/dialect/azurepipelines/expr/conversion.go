package expr

import (
	"math/big"
	"strings"
	"time"
)

func ordinalFold(s string) string { return strings.ToUpper(s) }
func truthy(v Value) bool {
	switch v := v.(type) {
	case Null:
		return false
	case Bool:
		return bool(v)
	case String:
		return v != ""
	case Number:
		return v.rat().Sign() != 0
	default:
		return true
	}
}
func newIntegerQuotient(r *big.Rat) *big.Int { return new(big.Int).Quo(r.Num(), r.Denom()) }
func toNumber(v Value) (Number, bool) {
	switch v := v.(type) {
	case Number:
		return v, true
	case Null:
		return integer(0), true
	case Bool:
		if v {
			return integer(1), true
		}
		return integer(0), true
	case String:
		if v == "" {
			return integer(0), true
		}
		s := strings.TrimSpace(string(v))
		s = strings.TrimPrefix(s, "+")
		// NumberStyles.AllowThousands applies only to the integer portion.
		whole, fraction, point := strings.Cut(s, ".")
		unsigned := strings.TrimPrefix(whole, "-")
		if strings.HasPrefix(unsigned, ",") || strings.Contains(fraction, ",") {
			return Number{}, false
		}
		s = strings.ReplaceAll(whole, ",", "")
		if point {
			s += "." + fraction
		}
		n, err := ParseNumber(s)
		return n, err == nil
	default:
		return Number{}, false
	}
}

func convertLike(value, target Value) (Value, bool) {
	switch target.(type) {
	case Null:
		_, null := value.(Null)
		s, empty := value.(String)
		return Null{}, null || empty && s == ""
	case Bool:
		return Bool(truthy(value)), true
	case Number:
		return toNumber(value)
	case String:
		s, ok := stringify(value)
		return String(s), ok
	case Version:
		switch v := value.(type) {
		case Version:
			return v, true
		case String:
			r, err := parseVersion(strings.TrimSpace(string(v)))
			return r, err == nil
		case Number:
			if v.rat().Sign() > 0 {
				r, err := parseVersion(v.String())
				return r, err == nil
			}
		}
	case DateTime:
		if v, ok := value.(DateTime); ok {
			return v, true
		}
		if v, ok := value.(String); ok {
			t, err := time.Parse(time.RFC3339Nano, strings.Replace(string(v), " ", "T", 1))
			return DateTime{t}, err == nil
		}
	case *Array:
		_, ok := value.(*Array)
		return value, ok
	case *projection:
		_, ok := value.(*projection)
		return value, ok
	case *Object:
		_, ok := value.(*Object)
		return value, ok
	}
	return Null{}, false
}

func equalConverted(left, right Value) (bool, bool) {
	converted, ok := convertLike(right, left)
	if !ok {
		return false, false
	}
	switch l := left.(type) {
	case Null:
		return true, true
	case Bool:
		return l == converted.(Bool), true
	case String:
		return ordinalFold(string(l)) == ordinalFold(string(converted.(String))), true
	case Number:
		return l.rat().Cmp(converted.(Number).rat()) == 0, true
	case Version:
		return compareVersions(l, converted.(Version)) == 0, true
	case DateTime:
		return l.Time.Equal(converted.(DateTime).Time), true
	case *Array:
		return l == converted.(*Array), true
	case *projection:
		return l == converted.(*projection), true
	case *Object:
		return l == converted.(*Object), true
	default:
		return false, false
	}
}

func compareConverted(left, right Value) (int, bool) {
	// Relational null follows the reference engine's numeric fallback, unlike equality.
	if _, ok := left.(Null); ok {
		left = integer(0)
	}
	converted, ok := convertLike(right, left)
	if !ok {
		return 0, false
	}
	switch l := left.(type) {
	case String:
		return strings.Compare(ordinalFold(string(l)), ordinalFold(string(converted.(String)))), true
	case Number:
		return l.rat().Cmp(converted.(Number).rat()), true
	case Version:
		return compareVersions(l, converted.(Version)), true
	case DateTime:
		return l.Time.Compare(converted.(DateTime).Time), true
	case Bool:
		r := converted.(Bool)
		if l == r {
			return 0, true
		}
		if !l {
			return -1, true
		}
		return 1, true
	default:
		return 0, false
	}
}

func compareVersions(left, right Version) int {
	for i := 0; i < 4; i++ {
		l, r := -1, -1
		if i < len(left.Parts) {
			l = left.Parts[i]
		}
		if i < len(right.Parts) {
			r = right.Parts[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}
