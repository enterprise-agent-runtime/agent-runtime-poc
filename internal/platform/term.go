package platform

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// ReadSecretLine reads one line from f without echo when f is a terminal,
// and as a plain line otherwise (scripted setup). The trailing newline is
// removed. Used for API keys and the secrets passphrase (D-012).
func ReadSecretLine(f *os.File) ([]byte, error) {
	restore, err := disableEcho(f)
	if err == nil {
		defer restore()
	}
	r := bufio.NewReader(f)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	if restore != nil {
		os.Stderr.WriteString("\n")
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}
