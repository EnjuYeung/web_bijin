package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// Two-step verification uses RFC 6238 codes (HMAC-SHA1, 6 digits, 30-second
// steps): the kind 1Password and other authenticator apps show.
const codeStep = 30

var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// parseTwoStepSecret accepts the base32 text an authenticator app shows,
// ignoring spaces, case and padding; RFC 4226 asks for at least 128 bits.
func parseTwoStepSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "=", "").Replace(s))
	key, err := secretEncoding.DecodeString(s)
	if err != nil || len(key) < 16 {
		return nil, errors.New("AUTH_TWO_STEP_SECRET must be base32 text of at least 26 characters")
	}
	return key, nil
}

func newTwoStepSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return secretEncoding.EncodeToString(b), nil
}

func stepCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// codeStepAt returns the step whose code equals code, allowing one step
// either side of now for a phone clock that is slightly off.
func codeStepAt(key []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	t := now.Unix() / codeStep
	var found int64
	ok := false
	for s := t - 1; s <= t+1; s++ {
		if subtle.ConstantTimeCompare([]byte(stepCode(key, s)), []byte(code)) == 1 {
			found, ok = s, true
		}
	}
	return found, ok
}

// runTwoStepSetup shows a new secret as a QR code and text, then prints it
// to out only after a code from the app matches, so a bad scan changes
// nothing. scripts/two-step.sh writes the printed secret into .env.
func runTwoStepSetup(in io.Reader, out, msg io.Writer, secret, user string, now func() time.Time) int {
	key, err := parseTwoStepSecret(secret)
	if err != nil {
		fmt.Fprintln(msg, err)
		return 1
	}
	code, err := qr.Encode(otpauthURI(secret, user), qr.M)
	if err != nil {
		fmt.Fprintln(msg, err)
		return 1
	}
	fmt.Fprintln(msg, "用 1Password 或其他身份验证器扫描下面的二维码：")
	fmt.Fprint(msg, terminalQR(code))
	fmt.Fprintln(msg, "无法扫码时，手动输入密钥：", groupSecret(secret))
	lines := bufio.NewScanner(in)
	for try := 0; try < 3; try++ {
		fmt.Fprint(msg, "输入 App 上显示的 6 位动态码：")
		if !lines.Scan() {
			fmt.Fprintln(msg)
			break
		}
		if _, ok := codeStepAt(key, strings.TrimSpace(lines.Text()), now()); ok {
			fmt.Fprintln(out, secret)
			return 0
		}
		fmt.Fprintln(msg, "动态码不对，请确认已扫码并且手机时间准确。")
	}
	fmt.Fprintln(msg, "没有确认成功，设置没有任何改动。")
	return 1
}

// otpauthURI is the link a QR code carries for authenticator apps.
func otpauthURI(secret, user string) string {
	label := "bijin"
	if user != "" {
		label += ":" + user
	}
	return "otpauth://totp/" + url.PathEscape(label) + "?" + url.Values{
		"secret": {secret}, "issuer": {"bijin"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"},
	}.Encode()
}

// terminalQR draws two QR rows per text line with half blocks, in explicit
// black on white so it scans in dark terminals too.
func terminalQR(c *qr.Code) string {
	const quiet = 2
	black := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		for x := -quiet; x < c.Size+quiet; x++ {
			fg, bg := "97", "107"
			if black(x, y) {
				fg = "30"
			}
			if black(x, y+1) {
				bg = "40"
			}
			b.WriteString("\x1b[" + fg + ";" + bg + "m▀")
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

func groupSecret(s string) string {
	var parts []string
	for len(s) > 4 {
		parts = append(parts, s[:4])
		s = s[4:]
	}
	return strings.Join(append(parts, s), " ")
}
