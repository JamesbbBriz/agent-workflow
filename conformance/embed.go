package conformance

import _ "embed"

//go:embed fixtures/generic.json
var genericFixture []byte

func GenericFixture() []byte {
	return append([]byte(nil), genericFixture...)
}
