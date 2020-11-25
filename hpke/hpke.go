// Package hpke implements the Hybrid Public Key Encryption (HPKE) as specified
// by draft-irtf-cfrg-hpke-06.
//
// HPKE works for any combination of an asymmetric-key encapsulation mechanism
// (KEM), a key derivation function (KDF), and an authenticated symmetric-key
// encryption scheme with additional data (AEAD).
//
// Specification in
// https://www.ietf.org/archive/id/draft-irtf-cfrg-hpke-06.html
package hpke

import (
	"errors"

	"github.com/cloudflare/circl/kem"
)

const versionLabel = "HPKE-06"

// Exporter allows exporting secrets from an HPKE context using a
// variable-length PRF. Export takes as input a context string expCtx and a
// desired length (in bytes), and produces a secret derived from the internal
// exporter secret using the corresponding KDF Expand function.
type Exporter interface {
	Export(expCtx []byte, len uint16) []byte
}

// Sealer encrypts a plaintext using an AEAD encryption. The caller supplies
// the plaintext and associated data; the nonce is stored internally by the
// sealer and incremented after each call.
type Sealer interface {
	Exporter
	Seal(pt, aad []byte) (ct []byte, err error)
}

// Opener decrypts a ciphertext using an AEAD encryption. The caller supplies
// the ciphertext and associated data; the nonce is stored internally by the
// Opener and incremented after each call.
type Opener interface {
	Exporter
	Open(ct, aad []byte) (pt []byte, err error)
}

// modeID represents an HPKE variant.
type modeID = uint8

const (
	// modeBase provides hybrid public-key encryption.
	modeBase modeID = 0x00
	// modePSK provides hybrid public-key encryption with authentication using a
	// pre-shared key.
	modePSK modeID = 0x01
	// modeAuth provides hybrid public-key encryption with authentication using
	// the sender's secret key.
	modeAuth modeID = 0x02
	// modeAuthPSK provides hybrid public-key encryption with authentication
	// using both a pre-shared key and an asymmetric key.
	modeAuthPSK modeID = 0x03
)

// Suite is an HPKE cipher suite consisting of a KEM, KDF, and AEAD algorithm.
type Suite struct {
	KemID  KemID
	KdfID  KdfID
	AeadID AeadID
}

type state struct {
	Suite
	modeID modeID
	skS    kem.PrivateKey
	pkS    kem.PublicKey
	psk    []byte
	pskID  []byte
	info   []byte
}

// Sender performs hybrid public-key encryption.
type Sender struct {
	state
	pkR kem.PublicKey
}

// NewSender creates a Sender with knowledge of the receiver's public-key.
func (suite Suite) NewSender(pkR kem.PublicKey, info []byte) (*Sender, error) {
	if !suite.isValid() {
		return nil, errors.New("invalid suite")
	}
	return &Sender{
		state: state{Suite: suite, info: info},
		pkR:   pkR,
	}, nil
}

// Setup generates a new HPKE context used for Base Mode encryption.
// Returns the Sealer and corresponding encapsulated key.
func (s *Sender) Setup() (enc []byte, seal Sealer, err error) {
	return s.buildBase().allSetup(s.KemID.Scheme())
}

func (s *Sender) buildBase() *Sender {
	s.modeID = modeBase
	return s
}

// SetupAuth generates a new HPKE context used for Auth Mode encryption.
// Returns the Sealer and corresponding encapsulated key.
func (s *Sender) SetupAuth(skS kem.PrivateKey) (
	enc []byte, seal Sealer, err error,
) {
	return s.buildAuth(skS).allSetup(s.KemID.Scheme())
}

func (s *Sender) buildAuth(skS kem.PrivateKey) *Sender {
	s.modeID = modeAuth
	s.state.skS = skS
	return s
}

// SetupPSK generates a new HPKE context used for PSK Mode encryption.
// Returns the Sealer and corresponding encapsulated key.
func (s *Sender) SetupPSK(psk, pskID []byte) (
	enc []byte, seal Sealer, err error,
) {
	return s.buildPSK(psk, pskID).allSetup(s.KemID.Scheme())
}

func (s *Sender) buildPSK(psk, pskID []byte) *Sender {
	s.modeID = modePSK
	s.state.psk = psk
	s.state.pskID = pskID
	return s
}

// SetupAuthPSK generates a new HPKE context used for Auth-PSK Mode encryption.
// Returns the Sealer and corresponding encapsulated key.
func (s *Sender) SetupAuthPSK(skS kem.PrivateKey, psk, pskID []byte) (
	enc []byte, seal Sealer, err error,
) {
	return s.buildAuthPSK(skS, psk, pskID).allSetup(s.KemID.Scheme())
}

func (s *Sender) buildAuthPSK(skS kem.PrivateKey, psk, pskID []byte) *Sender {
	s.modeID = modeAuthPSK
	s.state.skS = skS
	s.state.psk = psk
	s.state.pskID = pskID
	return s
}

// Receiver performs hybrid public-key decryption.
type Receiver struct {
	state
	skR kem.PrivateKey
	enc []byte
}

// NewReceiver creates a Receiver with knwoledge of a private-key.
func (suite Suite) NewReceiver(
	skR kem.PrivateKey,
	info []byte,
) (*Receiver, error) {
	if !suite.isValid() {
		return nil, errors.New("invalid suite")
	}
	return &Receiver{state: state{Suite: suite, info: info}, skR: skR}, nil
}

// Setup generates a new HPKE context used for Base Mode encryption.
// Setup takes an encapsulated key and returns an opener.
func (r *Receiver) Setup(enc []byte) (Opener, error) {
	r.modeID = modeBase
	r.enc = enc
	return r.allSetup()
}

// SetupAuth generates a new HPKE context used for Auth Mode encryption.
// SetupAuth takes an encapsulated key and a public key, and returns an opener.
func (r *Receiver) SetupAuth(enc []byte, pkS kem.PublicKey) (Opener, error) {
	r.modeID = modeAuth
	r.enc = enc
	r.state.pkS = pkS
	return r.allSetup()
}

// SetupPSK generates a new HPKE context used for PSK Mode encryption.
// SetupPSK takes an encapsulated key, and a pre-shared key; and returns an
// opener.
func (r *Receiver) SetupPSK(enc, psk, pskID []byte) (Opener, error) {
	r.modeID = modePSK
	r.enc = enc
	r.state.psk = psk
	r.state.pskID = pskID
	return r.allSetup()
}

// SetupAuthPSK generates a new HPKE context used for Auth-PSK Mode encryption.
// SetupAuthPSK takes an encapsulated key, a public key, and a pre-shared key;
// and returns an opener.
func (r *Receiver) SetupAuthPSK(
	enc, psk, pskID []byte,
	pkS kem.PublicKey,
) (Opener, error) {
	r.modeID = modeAuthPSK
	r.enc = enc
	r.state.psk = psk
	r.state.pskID = pskID
	r.state.pkS = pkS
	return r.allSetup()
}

func (s *Sender) allSetup(k kem.AuthScheme) ([]byte, Sealer, error) {
	var err error
	var enc, ss []byte

	switch s.modeID {
	case modeBase, modePSK:
		enc, ss, err = k.Encapsulate(s.pkR)
	case modeAuth, modeAuthPSK:
		enc, ss, err = k.AuthEncapsulate(s.pkR, s.skS)
	}
	if err != nil {
		return nil, nil, err
	}

	ctx, err := s.keySchedule(ss, s.info, s.psk, s.pskID)
	if err != nil {
		return nil, nil, err
	}

	return enc, &sealCtx{ctx}, nil
}

func (r *Receiver) allSetup() (Opener, error) {
	var err error
	var ss []byte
	k := r.KemID.Scheme()
	switch r.modeID {
	case modeBase, modePSK:
		ss, err = k.Decapsulate(r.skR, r.enc)
	case modeAuth, modeAuthPSK:
		ss, err = k.AuthDecapsulate(r.skR, r.enc, r.pkS)
	}
	if err != nil {
		return nil, err
	}

	ctx, err := r.keySchedule(ss, r.info, r.psk, r.pskID)
	if err != nil {
		return nil, err
	}
	return &openCtx{ctx}, nil
}
