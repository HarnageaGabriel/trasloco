package expr

import (
	"fmt"
	"strconv"
	"strings"
)

func formatDate(date DateTime, spec string) (string, error) {
	var b strings.Builder
	t := date.Time
	for pos := 0; pos < len(spec); {
		c := spec[pos]
		if c == '\\' {
			pos++
			if pos == len(spec) {
				return "", fmt.Errorf("incomplete DateTime escape")
			}
			b.WriteByte(spec[pos])
			pos++
			continue
		}
		if c == ' ' {
			b.WriteByte(c)
			pos++
			continue
		}
		end := pos + 1
		for end < len(spec) && spec[end] == c {
			end++
		}
		token := spec[pos:end]
		pos = end
		switch token {
		case "yyyy":
			fmt.Fprintf(&b, "%04d", t.Year())
		case "yy":
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case "M":
			b.WriteString(strconv.Itoa(int(t.Month())))
		case "MM":
			b.WriteString(t.Format("01"))
		case "d":
			b.WriteString(strconv.Itoa(t.Day()))
		case "dd":
			b.WriteString(t.Format("02"))
		case "H":
			b.WriteString(strconv.Itoa(t.Hour()))
		case "HH":
			b.WriteString(t.Format("15"))
		case "m":
			b.WriteString(strconv.Itoa(t.Minute()))
		case "mm":
			b.WriteString(t.Format("04"))
		case "s":
			b.WriteString(strconv.Itoa(t.Second()))
		case "ss":
			b.WriteString(t.Format("05"))
		case "K":
			b.WriteString(t.Format("Z07:00"))
		case "zzz":
			b.WriteString(t.Format("-07:00"))
		default:
			if c == 'f' && len(token) <= 7 {
				fraction := fmt.Sprintf("%09d", t.Nanosecond())
				b.WriteString(fraction[:len(token)])
			} else {
				return "", fmt.Errorf("invalid DateTime specifier")
			}
		}
	}
	return b.String(), nil
}
