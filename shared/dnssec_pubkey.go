package shared

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"math/big"

	"github.com/miekg/dns"
)

// publicKeyFromPrivate derives the public key from the fields of a private key.
func publicKeyFromPrivate(algorithm uint8, fields map[string]string) (crypto.PublicKey, error) {
	decode := func(name string) ([]byte, error) {
		value, err := base64.StdEncoding.DecodeString(fields[name])
		if err == nil && len(value) == 0 {
			err = errors.New("key content has no " + name)
		}
		return value, err
	}

	switch algorithm {
	case dns.ECDSAP256SHA256, dns.ECDSAP384SHA384:
		private, err := decode("privatekey")
		if err != nil {
			return nil, err
		}

		curve, size := ecdh.P256(), 32
		if algorithm == dns.ECDSAP384SHA384 {
			curve, size = ecdh.P384(), 48
		}
		if len(private) > size {
			return nil, errors.New("invalid ECDSA private key")
		}
		key, err := curve.NewPrivateKey(append(make([]byte, size-len(private)), private...))
		if err != nil {
			return nil, err
		}

		// uncompressed point: 0x04 || X || Y
		point := key.PublicKey().Bytes()
		ellipticCurve := elliptic.P256()
		if algorithm == dns.ECDSAP384SHA384 {
			ellipticCurve = elliptic.P384()
		}
		return &ecdsa.PublicKey{
			Curve: ellipticCurve,
			X:     new(big.Int).SetBytes(point[1 : 1+size]),
			Y:     new(big.Int).SetBytes(point[1+size:]),
		}, nil

	case dns.ED25519:
		seed, err := decode("privatekey")
		if err != nil {
			return nil, err
		}
		if len(seed) != ed25519.SeedSize {
			return nil, errors.New("invalid Ed25519 private key")
		}
		return ed25519.NewKeyFromSeed(seed).Public(), nil

	case dns.RSASHA1, dns.RSASHA1NSEC3SHA1, dns.RSASHA256, dns.RSASHA512:
		modulus, err := decode("modulus")
		if err != nil {
			return nil, err
		}
		exponent, err := decode("publicexponent")
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(modulus),
			E: int(new(big.Int).SetBytes(exponent).Int64()),
		}, nil
	}

	return nil, errors.New("unsupported DNSSEC algorithm")
}

// setPublicKey fills in the public key field of a DNSKEY in its wire format
// (RFC 3110, RFC 6605, RFC 8080).
func setPublicKey(dnskey *dns.DNSKEY, public crypto.PublicKey) error {
	var wire []byte

	switch key := public.(type) {
	case *ecdsa.PublicKey:
		size := (key.Curve.Params().BitSize + 7) / 8
		wire = append(padded(key.X, size), padded(key.Y, size)...)

	case ed25519.PublicKey:
		wire = key

	case *rsa.PublicKey:
		exponent := big.NewInt(int64(key.E)).Bytes()
		if len(exponent) < 256 {
			wire = append([]byte{byte(len(exponent))}, exponent...)
		} else {
			wire = append([]byte{0, byte(len(exponent) >> 8), byte(len(exponent))}, exponent...)
		}
		wire = append(wire, key.N.Bytes()...)

	default:
		return errors.New("unsupported public key")
	}

	dnskey.PublicKey = base64.StdEncoding.EncodeToString(wire)
	return nil
}

func padded(value *big.Int, size int) []byte {
	out := make([]byte, size)
	value.FillBytes(out)
	return out
}
