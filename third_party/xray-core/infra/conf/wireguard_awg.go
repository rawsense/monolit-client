package conf

// monolit: параметры маскировки AmneziaWG для подключения wireguard.
// В конфиге они записаны именами из файла .conf (Jc, S1, H1, HeaderProtectionKey, RandomTrailers…),
// библиотеке amneziawg-go нужны строки настройки устройства (jc=…, header_protection_key=<hex>…).

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/common/errors"
)

type awgParam struct {
	key  string // имя в строке настройки
	kind string // uint | range | bool | key | chain
}

// порядок строк постоянный: одинаковый конфиг даёт одинаковую настройку
var awgOrder = []string{
	"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4",
	"i1", "i2", "i3", "i4", "i5", "headerprotectionkey", "contentpaddingaddition",
	"rekeyaftertime", "rekeytimeout", "rejectaftertime", "keepalivetimeout", "maxhandshakeattempts",
	"randomtrailers", "disablecookies",
}

var awgParams = map[string]awgParam{
	"jc": {"jc", "uint"}, "jmin": {"jmin", "uint"}, "jmax": {"jmax", "uint"},
	"s1": {"s1", "uint"}, "s2": {"s2", "uint"}, "s3": {"s3", "uint"}, "s4": {"s4", "uint"},
	"h1": {"h1", "range"}, "h2": {"h2", "range"}, "h3": {"h3", "range"}, "h4": {"h4", "range"},
	"i1": {"i1", "chain"}, "i2": {"i2", "chain"}, "i3": {"i3", "chain"}, "i4": {"i4", "chain"}, "i5": {"i5", "chain"},
	"headerprotectionkey":    {"header_protection_key", "key"},
	"contentpaddingaddition": {"content_padding_addition", "range"},
	"rekeyaftertime":         {"rekey_after_time", "range"},
	"rekeytimeout":           {"rekey_timeout", "range"},
	"rejectaftertime":        {"reject_after_time", "range"},
	"keepalivetimeout":       {"keepalive_timeout", "range"},
	"maxhandshakeattempts":   {"max_handshake_attempts", "range"},
	"randomtrailers":         {"random_trailers", "bool"},
	"disablecookies":         {"disable_cookies", "bool"},
}

var (
	awgRange = regexp.MustCompile(`^[0-9]+(-[0-9]+)?$`)
	awgChain = regexp.MustCompile(`^(<(b 0x([0-9a-fA-F]{2})+|r [0-9]+|rd [0-9]+|rc [0-9]+|t)>)+$`)
)

// IsAWGParam: имя из файла .conf — параметр маскировки AmneziaWG
func IsAWGParam(name string) bool {
	_, ok := awgParams[awgName(name)]
	return ok
}

func awgName(k string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "_", ""))
}

// buildAWG проверяет параметры и собирает из них строки настройки устройства.
// Значение попадает в настройку как есть, поэтому перевод строки и неизвестное имя — отказ
func buildAWG(in map[string]string) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	vals := map[string]string{}
	for k, v := range in {
		n := awgName(k)
		if _, ok := awgParams[n]; !ok {
			return "", errors.New("unknown AmneziaWG parameter: ", k)
		}
		if _, dup := vals[n]; dup {
			return "", errors.New("AmneziaWG parameter set twice: ", k)
		}
		vals[n] = strings.TrimSpace(v)
	}
	var b strings.Builder
	for _, n := range awgOrder {
		v, ok := vals[n]
		if !ok || v == "" {
			continue
		}
		p := awgParams[n]
		switch p.kind {
		case "uint":
			if _, err := strconv.ParseUint(v, 10, 32); err != nil {
				return "", errors.New("AmneziaWG ", p.key, ": not a number: ", v)
			}
		case "range":
			if !awgRange.MatchString(v) {
				return "", errors.New("AmneziaWG ", p.key, ": expected a number or a-b: ", v)
			}
		case "bool":
			switch strings.ToLower(v) {
			case "on", "true", "1", "yes":
				v = "true"
			case "off", "false", "0", "no":
				v = "false"
			default:
				return "", errors.New("AmneziaWG ", p.key, ": expected on or off: ", v)
			}
		case "key":
			raw, err := hex.DecodeString(v)
			if err != nil || len(raw) != 32 {
				raw, err = base64.StdEncoding.DecodeString(v)
			}
			if err != nil || len(raw) != 32 {
				return "", errors.New("AmneziaWG ", p.key, ": expected a 32-byte key")
			}
			v = hex.EncodeToString(raw)
		case "chain":
			if !awgChain.MatchString(v) {
				return "", errors.New("AmneziaWG ", p.key, ": bad packet description")
			}
		}
		b.WriteString(p.key + "=" + v + "\n")
	}
	return b.String(), nil
}
