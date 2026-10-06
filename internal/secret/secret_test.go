package secret

import "testing"

func TestNameAndParam(t *testing.T) {
	for _, n := range []string{"API_TOKEN", "password", "DB_PASSWD", "X-Api-Key", "Authorization", "client_secret", "Set-Cookie",
		"sessionId", "PRIVATE_KEY", "refreshToken", "otp", "x-amz-signature", "AWS_SESSION"} {
		if !Name(n) || !Param(n) {
			t.Errorf("%q should look secret", n)
		}
	}
	for _, n := range []string{"API_BASE_URL", "PORT", "limit", "sort", "name", "email", "Content-Type", "X-Request-Id", "page"} {
		if Name(n) || Param(n) {
			t.Errorf("%q should not look secret", n)
		}
	}
	if Name("code") || !Param("code") {
		t.Error(`"code" is secret only as a query parameter (OAuth callbacks)`)
	}
}
