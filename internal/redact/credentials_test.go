package redact_test

import (
	"strings"
	"testing"
	"time"
)

// The fixtures below are assembled from fragments so that no complete
// credential-shaped literal appears in source. Each stays under the
// entropy layer's threshold, so a case passes only if the rule under
// test fires.
var (
	fakeHex20      = strings.Join([]string{"3f9a", "c07e", "5b21", "d8e6", "4a90"}, "")
	fakeHex32      = strings.Join([]string{"9c2e", "07fa", "b513", "d864", "9c2e", "07fa", "a1b3", "c5d7"}, "")
	fakeAlnum16    = strings.Join([]string{"Qm7v", "Xk2p", "Rt9w", "Lz4n"}, "")
	fakeAlnum20    = strings.Join([]string{"Hq3Z", "tV8m", "Wc2r", "Nk5y", "Bd7s"}, "")
	fakeAlnum24    = strings.Join([]string{"Jx4k", "Tq8w", "Mz2p", "Jx4k", "Rv6n", "Hc3b"}, "")
	fakeAlnum32    = strings.Join([]string{"Wd5h", "Kp2s", "Xm8v", "Wd5h", "Kp2s", "Bn7r", "Xm8v", "Lc4y"}, "")
	fakeLetters12  = strings.Join([]string{"kQw", "ZrT", "mPx", "LvB"}, "")
	fakeWordyPass  = "Summer" + "2024!"
	fakeRepeatPass = "hunter2" + "hunter"
	fakeLoginHost  = "deploy@" + "10.20.30.40"
)

func TestSecretNamedKeyWithAnOpaqueValueIsRedacted(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "api_key with a 20-char hex value",
			input: "api_key: " + fakeHex20,
			want:  "api_key: REDACTED",
		},
		{
			name:  "secret assignment with a 16-char value",
			input: "secret=" + fakeAlnum16,
			want:  "secret=REDACTED",
		},
		{
			name:  "client_secret with a 24-char value",
			input: "client_secret: " + fakeAlnum24,
			want:  "client_secret: REDACTED",
		},
		{
			name:  "access_key with spaces around the equals sign",
			input: "access_key = " + fakeAlnum32,
			want:  "access_key = REDACTED",
		},
		{
			name:  "api key header",
			input: "X-Api-Key: " + fakeAlnum20,
			want:  "X-Api-Key: REDACTED",
		},
		{
			name:  "token in a JSON document written as text",
			input: `{"token": "` + fakeAlnum20 + `"}`,
			want:  `{"token": "REDACTED"}`,
		},
		{
			name:  "auth_token with a 32-char hex value",
			input: "auth_token: " + fakeHex32,
			want:  "auth_token: REDACTED",
		},
		{
			name:  "password with a 16-char value",
			input: "password: " + fakeAlnum16,
			want:  "password: REDACTED",
		},
		{
			name:  "password with a letters-only value not spelled as words",
			input: "password=" + fakeLetters12,
			want:  "password=REDACTED",
		},
		{
			name:  "camelCase key",
			input: "apiKey: " + fakeAlnum20,
			want:  "apiKey: REDACTED",
		},
		{
			name:  "quoted value",
			input: `secret: "` + fakeAlnum16 + `"`,
			want:  `secret: "REDACTED"`,
		},
		{
			name:  "Chinese password key with a full-width colon",
			input: "密码：" + fakeAlnum16,
			want:  "密码：REDACTED",
		},
		{
			name:  "trailing comma stays outside the masked value",
			input: "the api_key: " + fakeHex20 + ", then rotate it",
			want:  "the api_key: REDACTED, then rotate it",
		},
		{
			name:  "key named by a generic word after a separator",
			input: "SIGNING_KEY: " + fakeAlnum20,
			want:  "SIGNING_KEY: REDACTED",
		},
		{
			name:  "key named by a generic word as a camelCase hump",
			input: "encryptionKey: " + fakeHex20,
			want:  "encryptionKey: REDACTED",
		},
		{
			name:  "cloud console access key pair",
			input: "AK=" + fakeAlnum16 + "\nSK=" + fakeAlnum24,
			want:  "AK=REDACTED\nSK=REDACTED",
		},
		{
			name:  "key numbered with a trailing digit",
			input: "API_KEY_2: " + fakeHex20,
			want:  "API_KEY_2: REDACTED",
		},
		{
			name:  "markdown bold key and a value in backticks",
			input: "- **API_KEY**：`" + fakeAlnum20 + "`",
			want:  "- **API_KEY**：`REDACTED`",
		},
		{
			name:  "quoted value in backticks",
			input: "TOKEN='`" + fakeAlnum20 + "`'",
			want:  "TOKEN='`REDACTED`'",
		},
		{
			name:  "value cut off without its closing quote",
			input: `"auth_token": "` + fakeHex32 + "\nnext line",
			want:  `"auth_token": "REDACTED` + "\nnext line",
		},
		{
			name:  "key and value passed to a call",
			input: `os.environ.setdefault("API_KEY", "` + fakeAlnum20 + `")`,
			want:  `os.environ.setdefault("API_KEY", "REDACTED")`,
		},
		{
			name:  "index expression assignment",
			input: `os.environ["SECRET_TOKEN"] = "` + fakeAlnum20 + `"`,
			want:  `os.environ["SECRET_TOKEN"] = "REDACTED"`,
		},
		{
			name:  "query string separator ends the value",
			input: "api_key=" + fakeHex20 + "&format=json",
			want:  "api_key=REDACTED&format=json",
		},
		{
			name:  "second key after a short value in a query string",
			input: "https://h.example.com/v1?token=1&api_key=" + fakeAlnum16,
			want:  "https://h.example.com/v1?token=1&api_key=REDACTED",
		},
		{
			name:  "each key of an ampersand chain",
			input: "api_key=" + fakeHex20 + "&client_secret=" + fakeAlnum16,
			want:  "api_key=REDACTED&client_secret=REDACTED",
		},
		{
			name:  "second key of a comma chain",
			input: "token=abc,secret=" + fakeAlnum16,
			want:  "token=abc,secret=REDACTED",
		},
	})
}

func TestSecretNamedKeyOverRedactionGuards(t *testing.T) {
	t.Parallel()
	unchanged := []string{
		"api_key: <your-api-key>",
		"token: xxxxxxxxxxxx",
		"secret=changeme",
		"password: ${DB_PASS}",
		"api_key=$API_KEY",
		"token: REDACTED",
		"token: process.env.API_TOKEN",
		"token: string",
		"max_tokens: 4096",
		"secret: rotate-every-quarter-2024",
		"api_key: YOUR_API_KEY_HERE",
		"token: accessTokenProvider",
		"secret: /etc/app/secret.pem",
		"secret: prod/db/password",
		"api_key: https://vault.internal/keys/7",
		"password: " + fakeRepeatPass,
		"monkey: " + fakeAlnum16,
		"hotkeys: " + fakeAlnum16,
		`NEXT_PUBLIC_STRIPE_KEY="pk_live_` + fakeAlnum24 + `"`,
		`["token", "` + fakeAlnum20 + `"]`,
		"password_hash: " + fakeHex20,
		"secret: webhookSecretARN",
		"token: @scope/package-name",
		"--- PASS: TestJSONLBytesKeepsTheRecord/SecretNamedKey (0.00s)",
	}
	var cases []stringRedactionCase
	for _, in := range unchanged {
		cases = append(cases, stringRedactionCase{name: in + " is preserved", input: in, want: in})
	}
	assertFieldRedactionCases(t, cases)
}

func TestLoginPasswordIsRedacted(t *testing.T) {
	t.Parallel()
	var cases []stringRedactionCase
	for _, pw := range []string{fakeRepeatPass, fakeWordyPass, fakeAlnum16} {
		cases = append(cases,
			stringRedactionCase{
				name:  "password on the line after an ssh command/" + pw[:3],
				input: "ssh " + fakeLoginHost + " -p 2222\npassword: " + pw,
				want:  "ssh " + fakeLoginHost + " -p 2222\npassword: REDACTED",
			},
			stringRedactionCase{
				name:  "password after a login target on the same line/" + pw[:3],
				input: fakeLoginHost + ":2222 password: " + pw,
				want:  fakeLoginHost + ":2222 password: REDACTED",
			},
			stringRedactionCase{
				name:  "password word in prose after a login target/" + pw[:3],
				input: fakeLoginHost + ":2222 pass " + pw,
				want:  fakeLoginHost + ":2222 pass REDACTED",
			},
			stringRedactionCase{
				name:  "sshpass with a quoted password/" + pw[:3],
				input: "sshpass -p '" + pw + "' ssh " + fakeLoginHost + " -p 2222",
				want:  "sshpass -p 'REDACTED' ssh " + fakeLoginHost + " -p 2222",
			},
			stringRedactionCase{
				name:  "sshpass with an unquoted password after another flag/" + pw[:3],
				input: "sshpass -v -p " + pw + " ssh " + fakeLoginHost,
				want:  "sshpass -v -p REDACTED ssh " + fakeLoginHost,
			},
			stringRedactionCase{
				name:  "password three lines below a host/" + pw[:3],
				input: "host: 10.20.30.40\nport: 22\nuser: deploy\npassword: " + pw,
				want:  "host: 10.20.30.40\nport: 22\nuser: deploy\npassword: REDACTED",
			},
			stringRedactionCase{
				name:  "Chinese password word after a login target/" + pw[:3],
				input: fakeLoginHost + " 密码 " + pw,
				want:  fakeLoginHost + " 密码 REDACTED",
			},
		)
	}
	for _, pw := range []string{fakeWordyPass, fakeAlnum16} {
		cases = append(cases,
			stringRedactionCase{
				name:  "opaque token after a login target and port/" + pw[:3],
				input: fakeLoginHost + ":2222 " + pw,
				want:  fakeLoginHost + ":2222 REDACTED",
			},
			stringRedactionCase{
				name:  "opaque token after a named login target and port/" + pw[:3],
				input: "deploy@build-07.corp.internal:2222 " + pw + "\nthen run the job",
				want:  "[REDACTED_EMAIL]:2222 REDACTED\nthen run the job",
			},
		)
	}
	assertFieldRedactionCases(t, cases)
}

func TestLoginPasswordOverRedactionGuards(t *testing.T) {
	t.Parallel()
	unchanged := map[string]string{
		"password beyond the window of a host":                      "host: 10.20.30.40\na\nb\nc\nd\npassword: " + fakeRepeatPass,
		"word after a login target and port":                        "connect to " + fakeLoginHost + ":2222 now",
		"sshpass reading a variable":                                `sshpass -p "$SSHPASS" ssh ` + fakeLoginHost,
		"sshpass reading a file":                                    "sshpass -f ~/.ssh/pw ssh -p 2222 " + fakeLoginHost,
		"placeholder password beside a host":                        "ssh " + fakeLoginHost + "\npassword: <password>",
		"repeated password with no host nearby":                     "password: " + fakeRepeatPass,
		"one-letter placeholder handed to sshpass":                  "sshpass -p 'X' ssh " + fakeLoginHost,
		"password read from the environment beside a host":          "host: 10.20.30.40\npassword: os.Getenv(\"DB_PW\")",
		"password taken from a variable in a code literal":          "const host = '10.20.30.40';\nconst cfg = { password: hashedPassword, user };",
		"password built by concatenation beside a host":             "ssh " + fakeLoginHost + "\npassword: '\" + pw + \"'",
		"password word run into a longer word after a login target": "ssh " + fakeLoginHost + " passwordless login works",
		"pass run into a longer word after a login target":          fakeLoginHost + " passphrase prompt",
	}
	var cases []stringRedactionCase
	for name, in := range unchanged {
		cases = append(cases, stringRedactionCase{name: name + " is preserved", input: in, want: in})
	}
	assertFieldRedactionCases(t, cases)
}

func TestHostWindowOverManyPasswordLinesCostsLittleMoreThanOverOtherLines(t *testing.T) {
	if testing.Short() {
		t.Skip("scans two 3 MB values")
	}
	const lines = 200_000
	timed := func(line string) (string, time.Duration) {
		input := strings.Repeat(line+"\n", lines) + "10.0.0.1"
		began := time.Now()
		result := redactedField(t, input)
		return result, time.Since(began)
	}
	_, plain := timed("username: root")
	result, passwords := timed("password: root")
	if passwords > 4*plain {
		t.Errorf("%d password lines took %s, %d other lines took %s", lines, passwords, lines, plain)
	}
	if !strings.HasPrefix(result, "password: root\n") {
		t.Errorf("a password far from the host was masked: %q", result[:40])
	}
	if want := strings.Repeat("password: REDACTED\n", 3) + "10.0.0.1"; !strings.HasSuffix(result, "password: root\n"+want) {
		t.Errorf("the passwords beside the host were not masked: %q", result[len(result)-80:])
	}
}

// Pins a known gap: after a login target and port, a password that reads
// as words is not told apart from prose, so only an opaque one is masked.
func TestWordLikePasswordAfterALoginTargetIsNotRedacted(t *testing.T) {
	t.Parallel()
	in := fakeLoginHost + ":2222 " + fakeRepeatPass
	assertFieldRedactionCases(t, []stringRedactionCase{
		{name: "word-like password after a login target and port", input: in, want: in},
	})
}

func TestJSONLBytes_SecretNamedKeyVerdictAgreesInStructureAndInAnEmbeddedString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		value    string
		masked   bool
	}{
		{name: "UnderscoredAPIKey", document: `{"api_key":"` + fakeHex20 + `"}`, value: fakeHex20, masked: true},
		{name: "HeaderSpelling", document: `{"headers":{"X-Api-Key":"` + fakeAlnum20 + `"}}`, value: fakeAlnum20, masked: true},
		{name: "CamelCaseSecret", document: `{"clientSecret":"` + fakeAlnum24 + `"}`, value: fakeAlnum24, masked: true},
		{name: "SpacedToken", document: `{"auth token":"` + fakeHex32 + `"}`, value: fakeHex32, masked: true},
		{name: "PasswordWithAnOpaqueValue", document: `{"password":"` + fakeAlnum16 + `"}`, value: fakeAlnum16, masked: true},
		{name: "TokenHoldingAnIdentifier", document: `{"token":"accessTokenProvider"}`, value: "accessTokenProvider", masked: false},
		{name: "KeyThatOnlyEndsInKey", document: `{"monkey":"` + fakeAlnum16 + `"}`, value: fakeAlnum16, masked: false},
		{name: "GenericWordKey", document: `{"signing_key":"` + fakeAlnum20 + `"}`, value: fakeAlnum20, masked: true},
		{name: "NumberedKey", document: `{"TOKEN2":"` + fakeAlnum20 + `"}`, value: fakeAlnum20, masked: true},
		{name: "ArrayElementAfterASecretWord", document: `{"argv":["token","` + fakeAlnum20 + `"]}`, value: fakeAlnum20, masked: false},
		{name: "GenericKeyBesideALoginNote", document: `{"note":"ssh ` + fakeLoginHost + `","password":"` + fakeRepeatPass + `"}`, value: fakeRepeatPass, masked: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			structured := redactedString(t, tc.document)
			if masked := !strings.Contains(structured, tc.value); masked != tc.masked {
				t.Errorf("structured spelling masked = %t, want %t: %s", masked, tc.masked, structured)
			}
			embedded := redactedField(t, tc.document)
			if masked := !strings.Contains(embedded, tc.value); masked != tc.masked {
				t.Errorf("embedded spelling masked = %t, want %t: %s", masked, tc.masked, embedded)
			}
			if embedded != structured {
				t.Errorf("the document walk and the text rule read one document differently: %s against %s", structured, embedded)
			}
		})
	}
}

func TestJSONLBytes_SecretNamedKeyValueInArrayIsRedactedToo(t *testing.T) {
	t.Parallel()
	input := `{"auth":{"api_key":"` + fakeHex20 + `"},"argv":["curl","-H","` + fakeHex20 + `"]}`
	result := redactedString(t, input)
	if strings.Contains(result, fakeHex20) {
		t.Fatalf("the key survived somewhere in %s", result)
	}
	if !strings.Contains(result, `"curl"`) {
		t.Fatalf("masking swallowed the argument vector: %s", result)
	}
}
