package integration

import (
	"net/http/cookiejar"
)

// newJar returns a cookie jar for the test HTTP client. cookiejar.New with a nil
// options value never returns an error in the standard library.
func newJar() *cookiejar.Jar {
	jar, _ := cookiejar.New(nil)
	return jar
}
