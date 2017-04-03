// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package packet

import (
	"bytes"
	"crypto"
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha1"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/big"
	"strconv"
	"time"

	"github.com/keybase/go-crypto/brainpool"
	"github.com/keybase/go-crypto/curve25519"
	"github.com/keybase/go-crypto/ed25519"
	"github.com/keybase/go-crypto/openpgp/ecdh"
	"github.com/keybase/go-crypto/openpgp/elgamal"
	"github.com/keybase/go-crypto/openpgp/errors"
	"github.com/keybase/go-crypto/rsa"
)

var (
	// NIST curve P-256
	oidCurveP256 []byte = []byte{0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07}
	// NIST curve P-384
	oidCurveP384 []byte = []byte{0x2B, 0x81, 0x04, 0x00, 0x22}
	// NIST curve P-521
	oidCurveP521 []byte = []byte{0x2B, 0x81, 0x04, 0x00, 0x23}
	// Brainpool curve P-256r1
	oidCurveP256r1 []byte = []byte{0x2B, 0x24, 0x03, 0x03, 0x02, 0x08, 0x01, 0x01, 0x07}
	// Brainpool curve P-384r1
	oidCurveP384r1 []byte = []byte{0x2B, 0x24, 0x03, 0x03, 0x02, 0x08, 0x01, 0x01, 0x0B}
	// Brainpool curve P-512r1
	oidCurveP512r1 []byte = []byte{0x2B, 0x24, 0x03, 0x03, 0x02, 0x08, 0x01, 0x01, 0x0D}
	// EdDSA
	oidEdDSA []byte = []byte{0x2B, 0x06, 0x01, 0x04, 0x01, 0xDA, 0x47, 0x0F, 0x01}
	// cv25519
	oidCurve25519 []byte = []byte{0x2B, 0x06, 0x01, 0x04, 0x01, 0x97, 0x55, 0x01, 0x05, 0x01}
)

const maxOIDLength = 10

// ecdsaKey stores the algorithm-specific fields for ECDSA keys.
// as defined in RFC 6637, Section 9.
type ecdsaKey struct {
	// oid contains the OID byte sequence identifying the elliptic curve used
	oid []byte
	// p contains the elliptic curve point that represents the public key
	p parsedMPI
}

type edDSAkey struct {
	ecdsaKey
}

func (e *edDSAkey) Verify(payload []byte, r parsedMPI, s parsedMPI) bool {
	var sig [ed25519.SignatureSize]byte

	// NOTE: The first byte is 0x40 - MPI header
	// TODO: Maybe clean the code up and use 0x40 as a header when
	// reading and keep only actual number in p field. Find out how
	// other MPIs are stored.
	key := e.p.bytes[1:]

	// Note: it may happen that R + S do not form 64-byte signature buffer that
	// ed25519 expects, but because we copy it over to an array of exact size,
	// we will always pass correctly sized slice to Verify. Slice too short
	// would make ed25519 panic().
	n := copy(sig[:], r.bytes)
	copy(sig[n:], s.bytes)

	return ed25519.Verify(key, payload, sig[:])
}

// parseOID reads the OID for the curve as defined in RFC 6637, Section 9.
func parseOID(r io.Reader) (oid []byte, err error) {
	buf := make([]byte, maxOIDLength)
	if _, err = readFull(r, buf[:1]); err != nil {
		return
	}
	oidLen := buf[0]
	if int(oidLen) > len(buf) {
		err = errors.UnsupportedError("invalid oid length: " + strconv.Itoa(int(oidLen)))
		return
	}
	oid = buf[:oidLen]
	_, err = readFull(r, oid)
	return
}

func (f *ecdsaKey) parse(r io.Reader) (err error) {
	if f.oid, err = parseOID(r); err != nil {
		return err
	}
	f.p.bytes, f.p.bitLength, err = readMPI(r)
	return err
}

func (f *ecdsaKey) serialize(w io.Writer) (err error) {
	buf := make([]byte, maxOIDLength+1)
	buf[0] = byte(len(f.oid))
	copy(buf[1:], f.oid)
	if _, err = w.Write(buf[:len(f.oid)+1]); err != nil {
		return
	}
	return writeMPIs(w, f.p)
}

func getCurveByOid(oid []byte) elliptic.Curve {
	switch {
	case bytes.Equal(oid, oidCurveP256):
		return elliptic.P256()
	case bytes.Equal(oid, oidCurveP384):
		return elliptic.P384()
	case bytes.Equal(oid, oidCurveP521):
		return elliptic.P521()
	case bytes.Equal(oid, oidCurveP256r1):
		return brainpool.P256r1()
	case bytes.Equal(oid, oidCurveP384r1):
		return brainpool.P384r1()
	case bytes.Equal(oid, oidCurveP512r1):
		return brainpool.P512r1()
	case bytes.Equal(oid, oidCurve25519):
		return curve25519.Cv25519()
	default:
		return nil
	}
}

func (f *ecdsaKey) newECDSA() (*ecdsa.PublicKey, error) {
	var c = getCurveByOid(f.oid)
	// Curve25519 should not be used in ECDSA.
	if c == nil || bytes.Equal(f.oid, oidCurve25519) {
		return nil, errors.UnsupportedError(fmt.Sprintf("unsupported oid: %x", f.oid))
	}
	// Note: Unmarshal already checks if point is on curve.
	x, y := elliptic.Unmarshal(c, f.p.bytes)
	if x == nil {
		return nil, errors.UnsupportedError("failed to parse EC point")
	}
	return &ecdsa.PublicKey{Curve: c, X: x, Y: y}, nil
}

func (f *ecdsaKey) newECDH() (*ecdh.PublicKey, error) {
	var c = getCurveByOid(f.oid)
	if c == nil {
		return nil, errors.UnsupportedError(fmt.Sprintf("unsupported oid: %x", f.oid))
	}
	// ecdh.Unmarshal handles unmarshaling for all curve types. It
	// also checks if point is on curve.
	x, y := ecdh.Unmarshal(c, f.p.bytes)
	if x == nil {
		return nil, errors.UnsupportedError("failed to parse EC point")
	}
	return &ecdh.PublicKey{Curve: c, X: x, Y: y}, nil
}

func (f *ecdsaKey) byteLen() int {
	return 1 + len(f.oid) + 2 + len(f.p.bytes)
}

type kdfHashFunction byte
type kdfAlgorithm byte

// ecdhKdf stores key derivation function parameters
// used for ECDH encryption. See RFC 6637, Section 9.
type ecdhKdf struct {
	KdfHash kdfHashFunction
	KdfAlgo kdfAlgorithm
}

func (f *ecdhKdf) parse(r io.Reader) (err error) {
	buf := make([]byte, 1)
	if _, err = readFull(r, buf); err != nil {
		return
	}
	kdfLen := int(buf[0])
	if kdfLen < 3 {
		return errors.UnsupportedError("Unsupported ECDH KDF length: " + strconv.Itoa(kdfLen))
	}
	buf = make([]byte, kdfLen)
	if _, err = readFull(r, buf); err != nil {
		return
	}
	reserved := int(buf[0])
	f.KdfHash = kdfHashFunction(buf[1])
	f.KdfAlgo = kdfAlgorithm(buf[2])
	if reserved != 0x01 {
		return errors.UnsupportedError("Unsupported KDF reserved field: " + strconv.Itoa(reserved))
	}
	return
}

func (f *ecdhKdf) serialize(w io.Writer) (err error) {
	buf := make([]byte, 4)
	// See RFC 6637, Section 9, Algorithm-Specific Fields for ECDH keys.
	buf[0] = byte(0x03) // Length of the following fields
	buf[1] = byte(0x01) // Reserved for future extensions, must be 1 for now
	buf[2] = byte(f.KdfHash)
	buf[3] = byte(f.KdfAlgo)
	_, err = w.Write(buf[:])
	return
}

func (f *ecdhKdf) byteLen() int {
	return 4
}

// PublicKey represents an OpenPGP public key. See RFC 4880, section 5.5.2.
type PublicKey struct {
	CreationTime time.Time
	PubKeyAlgo   PublicKeyAlgorithm
	PublicKey    interface{} // *rsa.PublicKey, *dsa.PublicKey or *ecdsa.PublicKey
	Fingerprint  [20]byte
	KeyId        uint64
	IsSubkey     bool

	n, e, p, q, g, y parsedMPI

	// RFC 6637 fields
	ec   *ecdsaKey
	ecdh *ecdhKdf

	// EdDSA fields (no RFC available), uses ecdsa scaffolding
	edk *edDSAkey
}

// signingKey provides a convenient abstraction over signature verification
// for v3 and v4 public keys.
type signingKey interface {
	SerializeSignaturePrefix(io.Writer)
	serializeWithoutHeaders(io.Writer) error
}

func FromBig(n *big.Int) parsedMPI {
	return parsedMPI{
		bytes:     n.Bytes(),
		bitLength: uint16(n.BitLen()),
	}
}

func FromBytes(bytes []byte) parsedMPI {
	return parsedMPI{
		bytes:     bytes,
		bitLength: uint16(8 * len(bytes)),
	}
}

// NewRSAPublicKey returns a PublicKey that wraps the given rsa.PublicKey.
func NewRSAPublicKey(creationTime time.Time, pub *rsa.PublicKey) *PublicKey {
	pk := &PublicKey{
		CreationTime: creationTime,
		PubKeyAlgo:   PubKeyAlgoRSA,
		PublicKey:    pub,
		n:            FromBig(pub.N),
		e:            FromBig(big.NewInt(int64(pub.E))),
	}

	pk.setFingerPrintAndKeyId()
	return pk
}

// NewDSAPublicKey returns a PublicKey that wraps the given dsa.PublicKey.
func NewDSAPublicKey(creationTime time.Time, pub *dsa.PublicKey) *PublicKey {
	pk := &PublicKey{
		CreationTime: creationTime,
		PubKeyAlgo:   PubKeyAlgoDSA,
		PublicKey:    pub,
		p:            FromBig(pub.P),
		q:            FromBig(pub.Q),
		g:            FromBig(pub.G),
		y:            FromBig(pub.Y),
	}

	pk.setFingerPrintAndKeyId()
	return pk
}

// check EdDSA public key material.
// There is currently no RFC for it, but it doesn't mean it's not
// implemented or in use.
func (e *edDSAkey) check() error {
	if !bytes.Equal(e.oid, oidEdDSA) {
		return errors.UnsupportedError(fmt.Sprintf("Bad OID for EdDSA key: %v", e.oid))
	}
	if bLen := len(e.p.bytes); bLen != 33 { // 32 bytes for ed25519 key and 1 byte for 0x40 header
		return errors.UnsupportedError(fmt.Sprintf("Unexpected EdDSA public key length: %d", bLen))
	}
	return nil
}

// NewElGamalPublicKey returns a PublicKey that wraps the given elgamal.PublicKey.
func NewElGamalPublicKey(creationTime time.Time, pub *elgamal.PublicKey) *PublicKey {
	pk := &PublicKey{
		CreationTime: creationTime,
		PubKeyAlgo:   PubKeyAlgoElGamal,
		PublicKey:    pub,
		p:            FromBig(pub.P),
		g:            FromBig(pub.G),
		y:            FromBig(pub.Y),
	}

	pk.setFingerPrintAndKeyId()
	return pk
}

func NewECDSAPublicKey(creationTime time.Time, pub *ecdsa.PublicKey) *PublicKey {
	pk := &PublicKey{
		CreationTime: creationTime,
		PubKeyAlgo:   PubKeyAlgoECDSA,
		PublicKey:    pub,
		ec:           new(ecdsaKey),
	}
	switch pub.Curve {
	case elliptic.P256():
		pk.ec.oid = oidCurveP256
	case elliptic.P384():
		pk.ec.oid = oidCurveP384
	case elliptic.P521():
		pk.ec.oid = oidCurveP521
	case brainpool.P256r1():
		pk.ec.oid = oidCurveP256r1
	case brainpool.P384r1():
		pk.ec.oid = oidCurveP384r1
	case brainpool.P512r1():
		pk.ec.oid = oidCurveP512r1
	}
	pk.ec.p.bytes = elliptic.Marshal(pub.Curve, pub.X, pub.Y)
	pk.ec.p.bitLength = uint16(8 * len(pk.ec.p.bytes))

	pk.setFingerPrintAndKeyId()
	return pk
}

func (pk *PublicKey) parse(r io.Reader) (err error) {
	// RFC 4880, section 5.5.2
	var buf [6]byte
	_, err = readFull(r, buf[:])
	if err != nil {
		return
	}
	if buf[0] != 4 {
		return errors.UnsupportedError("public key version")
	}
	pk.CreationTime = time.Unix(int64(uint32(buf[1])<<24|uint32(buf[2])<<16|uint32(buf[3])<<8|uint32(buf[4])), 0)
	pk.PubKeyAlgo = PublicKeyAlgorithm(buf[5])
	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSAEncryptOnly, PubKeyAlgoRSASignOnly:
		err = pk.parseRSA(r)
	case PubKeyAlgoDSA:
		err = pk.parseDSA(r)
	case PubKeyAlgoElGamal:
		err = pk.parseElGamal(r)
	case PubKeyAlgoEdDSA:
		pk.edk = new(edDSAkey)
		if err = pk.edk.parse(r); err != nil {
			return err
		}
		err = pk.edk.check()
	case PubKeyAlgoECDSA:
		pk.ec = new(ecdsaKey)
		if err = pk.ec.parse(r); err != nil {
			return err
		}
		pk.PublicKey, err = pk.ec.newECDSA()
	case PubKeyAlgoECDH:
		pk.ec = new(ecdsaKey)
		if err = pk.ec.parse(r); err != nil {
			return
		}
		pk.ecdh = new(ecdhKdf)
		if err = pk.ecdh.parse(r); err != nil {
			return
		}
		pk.PublicKey, err = pk.ec.newECDH()
	default:
		err = errors.UnsupportedError("public key type: " + strconv.Itoa(int(pk.PubKeyAlgo)))
	}
	if err != nil {
		return
	}

	pk.setFingerPrintAndKeyId()
	return
}

func (pk *PublicKey) setFingerPrintAndKeyId() {
	// RFC 4880, section 12.2
	fingerPrint := sha1.New()
	pk.SerializeSignaturePrefix(fingerPrint)
	pk.serializeWithoutHeaders(fingerPrint)
	copy(pk.Fingerprint[:], fingerPrint.Sum(nil))
	pk.KeyId = binary.BigEndian.Uint64(pk.Fingerprint[12:20])
}

// parseRSA parses RSA public key material from the given Reader. See RFC 4880,
// section 5.5.2.
func (pk *PublicKey) parseRSA(r io.Reader) (err error) {
	pk.n.bytes, pk.n.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.e.bytes, pk.e.bitLength, err = readMPI(r)
	if err != nil {
		return
	}

	if len(pk.e.bytes) > 7 {
		err = errors.UnsupportedError("large public exponent")
		return
	}
	rsa := &rsa.PublicKey{
		N: new(big.Int).SetBytes(pk.n.bytes),
		E: 0,
	}
	for i := 0; i < len(pk.e.bytes); i++ {
		rsa.E <<= 8
		rsa.E |= int64(pk.e.bytes[i])
	}
	pk.PublicKey = rsa
	return
}

// parseDSA parses DSA public key material from the given Reader. See RFC 4880,
// section 5.5.2.
func (pk *PublicKey) parseDSA(r io.Reader) (err error) {
	pk.p.bytes, pk.p.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.q.bytes, pk.q.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.g.bytes, pk.g.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.y.bytes, pk.y.bitLength, err = readMPI(r)
	if err != nil {
		return
	}

	dsa := new(dsa.PublicKey)
	dsa.P = new(big.Int).SetBytes(pk.p.bytes)
	dsa.Q = new(big.Int).SetBytes(pk.q.bytes)
	dsa.G = new(big.Int).SetBytes(pk.g.bytes)
	dsa.Y = new(big.Int).SetBytes(pk.y.bytes)
	pk.PublicKey = dsa
	return
}

// parseElGamal parses ElGamal public key material from the given Reader. See
// RFC 4880, section 5.5.2.
func (pk *PublicKey) parseElGamal(r io.Reader) (err error) {
	pk.p.bytes, pk.p.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.g.bytes, pk.g.bitLength, err = readMPI(r)
	if err != nil {
		return
	}
	pk.y.bytes, pk.y.bitLength, err = readMPI(r)
	if err != nil {
		return
	}

	elgamal := new(elgamal.PublicKey)
	elgamal.P = new(big.Int).SetBytes(pk.p.bytes)
	elgamal.G = new(big.Int).SetBytes(pk.g.bytes)
	elgamal.Y = new(big.Int).SetBytes(pk.y.bytes)
	pk.PublicKey = elgamal
	return
}

// SerializeSignaturePrefix writes the prefix for this public key to the given Writer.
// The prefix is used when calculating a signature over this public key. See
// RFC 4880, section 5.2.4.
func (pk *PublicKey) SerializeSignaturePrefix(h io.Writer) {
	var pLength uint16
	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSAEncryptOnly, PubKeyAlgoRSASignOnly:
		pLength += 2 + uint16(len(pk.n.bytes))
		pLength += 2 + uint16(len(pk.e.bytes))
	case PubKeyAlgoDSA:
		pLength += 2 + uint16(len(pk.p.bytes))
		pLength += 2 + uint16(len(pk.q.bytes))
		pLength += 2 + uint16(len(pk.g.bytes))
		pLength += 2 + uint16(len(pk.y.bytes))
	case PubKeyAlgoElGamal:
		pLength += 2 + uint16(len(pk.p.bytes))
		pLength += 2 + uint16(len(pk.g.bytes))
		pLength += 2 + uint16(len(pk.y.bytes))
	case PubKeyAlgoECDSA:
		pLength += uint16(pk.ec.byteLen())
	case PubKeyAlgoECDH:
		pLength += uint16(pk.ec.byteLen())
		pLength += uint16(pk.ecdh.byteLen())
	case PubKeyAlgoEdDSA:
		pLength += uint16(pk.edk.byteLen())
	default:
		panic("unknown public key algorithm")
	}
	pLength += 6
	h.Write([]byte{0x99, byte(pLength >> 8), byte(pLength)})
	return
}

func (pk *PublicKey) Serialize(w io.Writer) (err error) {
	length := 6 // 6 byte header

	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSAEncryptOnly, PubKeyAlgoRSASignOnly:
		length += 2 + len(pk.n.bytes)
		length += 2 + len(pk.e.bytes)
	case PubKeyAlgoDSA:
		length += 2 + len(pk.p.bytes)
		length += 2 + len(pk.q.bytes)
		length += 2 + len(pk.g.bytes)
		length += 2 + len(pk.y.bytes)
	case PubKeyAlgoElGamal:
		length += 2 + len(pk.p.bytes)
		length += 2 + len(pk.g.bytes)
		length += 2 + len(pk.y.bytes)
	case PubKeyAlgoECDSA:
		length += pk.ec.byteLen()
	case PubKeyAlgoECDH:
		length += pk.ec.byteLen()
		length += pk.ecdh.byteLen()
	case PubKeyAlgoEdDSA:
		length += pk.edk.byteLen()
	default:
		panic("unknown public key algorithm")
	}

	packetType := packetTypePublicKey
	if pk.IsSubkey {
		packetType = packetTypePublicSubkey
	}
	err = serializeHeader(w, packetType, length)
	if err != nil {
		return
	}
	return pk.serializeWithoutHeaders(w)
}

// serializeWithoutHeaders marshals the PublicKey to w in the form of an
// OpenPGP public key packet, not including the packet header.
func (pk *PublicKey) serializeWithoutHeaders(w io.Writer) (err error) {
	var buf [6]byte
	buf[0] = 4
	t := uint32(pk.CreationTime.Unix())
	buf[1] = byte(t >> 24)
	buf[2] = byte(t >> 16)
	buf[3] = byte(t >> 8)
	buf[4] = byte(t)
	buf[5] = byte(pk.PubKeyAlgo)

	_, err = w.Write(buf[:])
	if err != nil {
		return
	}

	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSAEncryptOnly, PubKeyAlgoRSASignOnly:
		return writeMPIs(w, pk.n, pk.e)
	case PubKeyAlgoDSA:
		return writeMPIs(w, pk.p, pk.q, pk.g, pk.y)
	case PubKeyAlgoElGamal:
		return writeMPIs(w, pk.p, pk.g, pk.y)
	case PubKeyAlgoECDSA:
		return pk.ec.serialize(w)
	case PubKeyAlgoEdDSA:
		return pk.edk.serialize(w)
	case PubKeyAlgoECDH:
		if err = pk.ec.serialize(w); err != nil {
			return
		}
		return pk.ecdh.serialize(w)
	}
	return errors.InvalidArgumentError("bad public-key algorithm")
}

// CanSign returns true iff this public key can generate signatures
func (pk *PublicKey) CanSign() bool {
	return pk.PubKeyAlgo != PubKeyAlgoRSAEncryptOnly && pk.PubKeyAlgo != PubKeyAlgoElGamal
}

// VerifySignature returns nil if sig is a valid signature, made by this
// public key, of the data hashed into signed. signed is mutated by this call.
func (pk *PublicKey) VerifySignature(signed hash.Hash, sig *Signature) (err error) {
	if !pk.CanSign() {
		return errors.InvalidArgumentError("public key cannot generate signatures")
	}

	signed.Write(sig.HashSuffix)
	hashBytes := signed.Sum(nil)

	// NOTE(maxtaco) 2016-08-22
	//
	// We used to do this:
	//
	// if hashBytes[0] != sig.HashTag[0] || hashBytes[1] != sig.HashTag[1] {
	//	  return errors.SignatureError("hash tag doesn't match")
	// }
	//
	// But don't do anything in this case. Some GPGs generate bad
	// 2-byte hash prefixes, but GPG also doesn't seem to care on
	// import. See BrentMaxwell's key. I think it's safe to disable
	// this check!

	if pk.PubKeyAlgo != sig.PubKeyAlgo {
		return errors.InvalidArgumentError("public key and signature use different algorithms")
	}

	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSASignOnly:
		rsaPublicKey, _ := pk.PublicKey.(*rsa.PublicKey)
		err = rsa.VerifyPKCS1v15(rsaPublicKey, sig.Hash, hashBytes, sig.RSASignature.bytes)
		if err != nil {
			return errors.SignatureError("RSA verification failure")
		}
		return nil
	case PubKeyAlgoDSA:
		dsaPublicKey, _ := pk.PublicKey.(*dsa.PublicKey)
		// Need to truncate hashBytes to match FIPS 186-3 section 4.6.
		subgroupSize := (dsaPublicKey.Q.BitLen() + 7) / 8
		if len(hashBytes) > subgroupSize {
			hashBytes = hashBytes[:subgroupSize]
		}
		if !dsa.Verify(dsaPublicKey, hashBytes, new(big.Int).SetBytes(sig.DSASigR.bytes), new(big.Int).SetBytes(sig.DSASigS.bytes)) {
			return errors.SignatureError("DSA verification failure")
		}
		return nil
	case PubKeyAlgoECDSA:
		ecdsaPublicKey := pk.PublicKey.(*ecdsa.PublicKey)
		if !ecdsa.Verify(ecdsaPublicKey, hashBytes, new(big.Int).SetBytes(sig.ECDSASigR.bytes), new(big.Int).SetBytes(sig.ECDSASigS.bytes)) {
			return errors.SignatureError("ECDSA verification failure")
		}
		return nil
	case PubKeyAlgoEdDSA:
		if !pk.edk.Verify(hashBytes, sig.EdDSASigR, sig.EdDSASigS) {
			return errors.SignatureError("EdDSA verification failure")
		}
		return nil
	default:
		return errors.SignatureError("Unsupported public key algorithm used in signature")
	}
	panic("unreachable")
}

// VerifySignatureV3 returns nil iff sig is a valid signature, made by this
// public key, of the data hashed into signed. signed is mutated by this call.
func (pk *PublicKey) VerifySignatureV3(signed hash.Hash, sig *SignatureV3) (err error) {
	if !pk.CanSign() {
		return errors.InvalidArgumentError("public key cannot generate signatures")
	}

	suffix := make([]byte, 5)
	suffix[0] = byte(sig.SigType)
	binary.BigEndian.PutUint32(suffix[1:], uint32(sig.CreationTime.Unix()))
	signed.Write(suffix)
	hashBytes := signed.Sum(nil)

	if hashBytes[0] != sig.HashTag[0] || hashBytes[1] != sig.HashTag[1] {
		return errors.SignatureError("hash tag doesn't match")
	}

	if pk.PubKeyAlgo != sig.PubKeyAlgo {
		return errors.InvalidArgumentError("public key and signature use different algorithms")
	}

	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSASignOnly:
		rsaPublicKey := pk.PublicKey.(*rsa.PublicKey)
		if err = rsa.VerifyPKCS1v15(rsaPublicKey, sig.Hash, hashBytes, sig.RSASignature.bytes); err != nil {
			return errors.SignatureError("RSA verification failure")
		}
		return
	case PubKeyAlgoDSA:
		dsaPublicKey := pk.PublicKey.(*dsa.PublicKey)
		// Need to truncate hashBytes to match FIPS 186-3 section 4.6.
		subgroupSize := (dsaPublicKey.Q.BitLen() + 7) / 8
		if len(hashBytes) > subgroupSize {
			hashBytes = hashBytes[:subgroupSize]
		}
		if !dsa.Verify(dsaPublicKey, hashBytes, new(big.Int).SetBytes(sig.DSASigR.bytes), new(big.Int).SetBytes(sig.DSASigS.bytes)) {
			return errors.SignatureError("DSA verification failure")
		}
		return nil
	default:
		panic("shouldn't happen")
	}
	panic("unreachable")
}

// keySignatureHash returns a Hash of the message that needs to be signed for
// pk to assert a subkey relationship to signed.
func keySignatureHash(pk, signed signingKey, hashFunc crypto.Hash) (h hash.Hash, err error) {
	if !hashFunc.Available() {
		return nil, errors.UnsupportedError("hash function")
	}
	h = hashFunc.New()

	updateKeySignatureHash(pk, signed, h)

	return
}

// updateKeySignatureHash does the actual hash updates for keySignatureHash.
func updateKeySignatureHash(pk, signed signingKey, h hash.Hash) {
	// RFC 4880, section 5.2.4
	pk.SerializeSignaturePrefix(h)
	pk.serializeWithoutHeaders(h)
	signed.SerializeSignaturePrefix(h)
	signed.serializeWithoutHeaders(h)
}

// VerifyKeySignature returns nil if sig is a valid signature, made by this
// public key, of signed.
func (pk *PublicKey) VerifyKeySignature(signed *PublicKey, sig *Signature) error {
	h, err := keySignatureHash(pk, signed, sig.Hash)
	if err != nil {
		return err
	}
	if err = pk.VerifySignature(h, sig); err != nil {
		return err
	}

	if sig.FlagSign {

		// BUG(maxtaco)
		//
		// We should check for more than FlagsSign here, because if
		// you read keys.go, we can sometimes use signing subkeys even if they're
		// not explicitly flagged as such. However, so doing fails lots of currently
		// working tests, so I'm not going to do much here.
		//
		// In other words, we should have this disjunction in the condition above:
		//
		//    || (!sig.FlagsValid && pk.PubKeyAlgo.CanSign()) {
		//

		// Signing subkeys must be cross-signed. See
		// https://www.gnupg.org/faq/subkey-cross-certify.html.
		if sig.EmbeddedSignature == nil {
			return errors.StructuralError("signing subkey is missing cross-signature")
		}
		// Verify the cross-signature. This is calculated over the same
		// data as the main signature, so we cannot just recursively
		// call signed.VerifyKeySignature(...)
		if h, err = keySignatureHash(pk, signed, sig.EmbeddedSignature.Hash); err != nil {
			return errors.StructuralError("error while hashing for cross-signature: " + err.Error())
		}
		if err := signed.VerifySignature(h, sig.EmbeddedSignature); err != nil {
			return errors.StructuralError("error while verifying cross-signature: " + err.Error())
		}
	}

	return nil
}

func keyRevocationHash(pk signingKey, hashFunc crypto.Hash) (h hash.Hash, err error) {
	if !hashFunc.Available() {
		return nil, errors.UnsupportedError("hash function")
	}
	h = hashFunc.New()

	// RFC 4880, section 5.2.4
	pk.SerializeSignaturePrefix(h)
	pk.serializeWithoutHeaders(h)

	return
}

// VerifyRevocationSignature returns nil if sig is a valid signature, made by this
// public key.
func (pk *PublicKey) VerifyRevocationSignature(sig *Signature) (err error) {
	h, err := keyRevocationHash(pk, sig.Hash)
	if err != nil {
		return err
	}
	return pk.VerifySignature(h, sig)
}

type teeHash struct {
	h hash.Hash
}

func (t teeHash) Write(b []byte) (n int, err error) {
	fmt.Printf("hash -> %s %+v\n", string(b), b)
	return t.h.Write(b)
}
func (t teeHash) Sum(b []byte) []byte { return t.h.Sum(b) }
func (t teeHash) Reset()              { t.h.Reset() }
func (t teeHash) Size() int           { return t.h.Size() }
func (t teeHash) BlockSize() int      { return t.h.BlockSize() }

// userIdSignatureHash returns a Hash of the message that needs to be signed
// to assert that pk is a valid key for id.
func userIdSignatureHash(id string, pk *PublicKey, hashFunc crypto.Hash) (h hash.Hash, err error) {
	if !hashFunc.Available() {
		return nil, errors.UnsupportedError("hash function")
	}
	h = hashFunc.New()

	updateUserIdSignatureHash(id, pk, h)

	return
}

// updateUserIdSignatureHash does the actual hash updates for
// userIdSignatureHash.
func updateUserIdSignatureHash(id string, pk *PublicKey, h hash.Hash) {
	// RFC 4880, section 5.2.4
	pk.SerializeSignaturePrefix(h)
	pk.serializeWithoutHeaders(h)

	var buf [5]byte
	buf[0] = 0xb4
	buf[1] = byte(len(id) >> 24)
	buf[2] = byte(len(id) >> 16)
	buf[3] = byte(len(id) >> 8)
	buf[4] = byte(len(id))
	h.Write(buf[:])
	h.Write([]byte(id))

	return
}

// VerifyUserIdSignature returns nil if sig is a valid signature, made by this
// public key, that id is the identity of pub.
func (pk *PublicKey) VerifyUserIdSignature(id string, pub *PublicKey, sig *Signature) (err error) {
	h, err := userIdSignatureHash(id, pub, sig.Hash)
	if err != nil {
		return err
	}
	return pk.VerifySignature(h, sig)
}

// VerifyUserIdSignatureV3 returns nil if sig is a valid signature, made by this
// public key, that id is the identity of pub.
func (pk *PublicKey) VerifyUserIdSignatureV3(id string, pub *PublicKey, sig *SignatureV3) (err error) {
	h, err := userIdSignatureV3Hash(id, pub, sig.Hash)
	if err != nil {
		return err
	}
	return pk.VerifySignatureV3(h, sig)
}

// KeyIdString returns the public key's fingerprint in capital hex
// (e.g. "6C7EE1B8621CC013").
func (pk *PublicKey) KeyIdString() string {
	return fmt.Sprintf("%X", pk.Fingerprint[12:20])
}

// KeyIdShortString returns the short form of public key's fingerprint
// in capital hex, as shown by gpg --list-keys (e.g. "621CC013").
func (pk *PublicKey) KeyIdShortString() string {
	return fmt.Sprintf("%X", pk.Fingerprint[16:20])
}

// A parsedMPI is used to store the contents of a big integer, along with the
// bit length that was specified in the original input. This allows the MPI to
// be reserialized exactly.
type parsedMPI struct {
	bytes     []byte
	bitLength uint16
}

// writeMPIs is a utility function for serializing several big integers to the
// given Writer.
func writeMPIs(w io.Writer, mpis ...parsedMPI) (err error) {
	for _, mpi := range mpis {
		err = writeMPI(w, mpi.bitLength, mpi.bytes)
		if err != nil {
			return
		}
	}
	return
}

// BitLength returns the bit length for the given public key. Used for
// displaying key information, actual buffers and BigInts inside may
// have non-matching different size if the key is invalid.
func (pk *PublicKey) BitLength() (bitLength uint16, err error) {
	switch pk.PubKeyAlgo {
	case PubKeyAlgoRSA, PubKeyAlgoRSAEncryptOnly, PubKeyAlgoRSASignOnly:
		bitLength = pk.n.bitLength
	case PubKeyAlgoDSA:
		bitLength = pk.p.bitLength
	case PubKeyAlgoElGamal:
		bitLength = pk.p.bitLength
	case PubKeyAlgoECDH:
		ecdhPublicKey := pk.PublicKey.(*ecdh.PublicKey)
		bitLength = uint16(ecdhPublicKey.Curve.Params().BitSize)
	case PubKeyAlgoECDSA:
		ecdsaPublicKey := pk.PublicKey.(*ecdsa.PublicKey)
		bitLength = uint16(ecdsaPublicKey.Curve.Params().BitSize)
	case PubKeyAlgoEdDSA:
		// EdDSA only support ed25519 curves right now, just return
		// the length. Also, we don't have any PublicKey.Curve object
		// to look the size up from.
		bitLength = 256
	default:
		err = errors.InvalidArgumentError("bad public-key algorithm")
	}
	return
}
