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
// Captured live from opencode CLI 1.18.31 (Bun/BoringSSL) via a transparent
// CONNECT relay 2026-09-17. Wire order matters: extensions are NOT shuffled
// (Bun sends a fixed order, no GREASE).
//
// JA3: 771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0
func bunSpecForConn() *utls.ClientHelloSpec {
	spec := utls.ClientHelloSpec{
		CipherSuites: []uint16{
		0x1301, // TLS_AES_128_GCM_SHA256 (BoringSSL order: AES128 first)
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1304, // TLS_AES_128_CCM_SHA256 (BoringSSL-only, no stdlib const)
		utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, // 49195
		utls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,   // 49199
		utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, // 49196
		utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,   // 49200
		utls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,    // 52393
		utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,  // 52392
		0xC0A8, // TLS_ECDHE_RSA_WITH_AES_128_CCM8 (BoringSSL-only, no stdlib const)
		0xC0A9, // TLS_ECDHE_RSA_WITH_AES_256_CCM8
		0xC0AC, // TLS_ECDHE_ECDSA_WITH_AES_128_CCM8
		0xC0AD, // TLS_ECDHE_ECDSA_WITH_AES_256_CCM8
		utls.TLS_RSA_WITH_AES_128_GCM_SHA256,         // 156
		utls.TLS_RSA_WITH_AES_256_GCM_SHA384,         // 157
		utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,      // 47
		utls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,      // 53
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



