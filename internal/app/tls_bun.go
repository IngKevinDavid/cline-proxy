package app

import (
	utls "github.com/refraction-networking/utls"
)

// bunSpecForConn returns a FRESH ClientHelloSpec per connection.
//
// The spec holds mutable per-handshake state: ApplyPreset auto-generates the
// X25519 key share into KeyShares[0].Data and ApplyConfig/session sync mutate
// extension objects in place. Sharing one global spec across concurrent (or
// even sequential) handshakes races on that state — the losers send malformed
// hellos and fail with "local error: tls: internal error". A fresh deep copy
// per DialTLS keeps the wire bytes identical (verified byte-for-byte against
// the official CLI capture modulo random/session/keyshare) and is race-free.
//
// Captured live from opencode CLI 1.18.31 (Bun/BoringSSL) via a CONNECT
// relay 2026-09-18 (SNI opencode.ai, true zen hello — not the registry fetch
// that an earlier capture had mistaken for zen traffic). Byte-verified
// against the CLI capture: cipher list, extension order, and every extension
// body identical modulo random/session/keyshare/padding-length.
//
// JA3: 771,4865-4866-4919-49243-49247-49244-49248-52394-52392-49209-49211-49192-49214-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0
func bunSpecForConn() *utls.ClientHelloSpec {
	spec := utls.ClientHelloSpec{
		CipherSuites: []uint16{
			0x1301, // TLS_AES_128_GCM_SHA256
			0x1302, // TLS_AES_256_GCM_SHA384
			0x1303, // TLS_CHACHA20_POLY1305_SHA256 (CLI sends 1303, NOT 1304/CCM8)
			0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
			0xC02F, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
			0xC02C, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
			0xC030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
			0xCCA9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305
			0xCCA8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305
			0xC009, // TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA
			0xC013, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
			0xC00A, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA
			0xC014, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA
			0x009C, // TLS_RSA_WITH_AES_128_GCM_SHA256
			0x009D, // TLS_RSA_WITH_AES_256_GCM_SHA384
			0x002F, // TLS_RSA_WITH_AES_128_CBC_SHA
			0x0035, // TLS_RSA_WITH_AES_256_CBC_SHA
		},
	CompressionMethods: []uint8{0x00},
	Extensions: []utls.TLSExtension{
		&utls.SNIExtension{},
		&utls.ExtendedMasterSecretExtension{},
		&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
			utls.X25519,   // 29
			utls.CurveP256, // 23
			utls.CurveP384, // 24
		}},
		&utls.SupportedPointsExtension{SupportedPoints: []byte{0x00}},
		&utls.SessionTicketExtension{},
		&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
		&utls.StatusRequestExtension{},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
			utls.ECDSAWithP256AndSHA256, // 1027
			utls.PSSWithSHA256,          // 2052
			utls.PKCS1WithSHA256,        // 1025
			utls.ECDSAWithP384AndSHA384, // 1283
			utls.PSSWithSHA384,          // 2053
			utls.PKCS1WithSHA384,        // 1281
			utls.PSSWithSHA512,          // 2054
			utls.PKCS1WithSHA512,        // 1537
			utls.PKCS1WithSHA1,          // 513
		}},
		&utls.SCTExtension{},
		// Empty Data = auto-generate a fresh X25519 share per handshake.
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
			{Group: utls.X25519},
		}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.SupportedVersionsExtension{Versions: []uint16{
			utls.VersionTLS13, // 772
			utls.VersionTLS12, // 771
		}},
		&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		},
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
	}
	return &spec
}



