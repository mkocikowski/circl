package hpke

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/cloudflare/circl/internal/test"
	"github.com/cloudflare/circl/kem"
)

func TestVectors(t *testing.T) {
	// Test vectors from
	// https://github.com/cfrg/draft-irtf-cfrg-hpke/blob/draft-irtf-cfrg-hpke-06/test-vectors.json
	vectors := readFile(t, "testdata/vectors_v06.json")
	for i, v := range vectors {
		t.Run(fmt.Sprintf("v%v", i), v.verify)
	}
}

func (v *vector) verify(t *testing.T) {
	m := v.ModeID
	s := Suite{KemID(v.KemID), KdfID(v.KdfID), AeadID(v.AeadID)}

	seed := hexB(v.IkmE)
	dhkem := s.KemID.Scheme()
	seededKem := seededKem{seed, dhkem}
	sender, recv := v.getActors(t, seededKem, s)
	sealer, opener := v.setup(t, seededKem, sender, recv, m, s)

	v.checkAead(t, (sealer.(*sealCtx)).encdecCtx, m, s)
	v.checkAead(t, (opener.(*openCtx)).encdecCtx, m, s)
	v.checkEncryptions(t, sealer, opener, m, s)
	v.checkExports(t, sealer, m, s)
	v.checkExports(t, opener, m, s)
}

func (v *vector) getActors(
	t *testing.T,
	dhkem kem.Scheme,
	s Suite,
) (*Sender, *Receiver) {
	h := fmt.Sprintf("%v\n", s)

	pkR, err := dhkem.UnmarshalBinaryPublicKey(hexB(v.PkRm))
	test.CheckNoErr(t, err, h+"bad public key")

	skR, err := dhkem.UnmarshalBinaryPrivateKey(hexB(v.SkRm))
	test.CheckNoErr(t, err, h+"bad private key")

	info := hexB(v.Info)
	sender, err := s.NewSender(pkR, info)
	test.CheckNoErr(t, err, h+"err sender")

	recv, err := s.NewReceiver(skR, info)
	test.CheckNoErr(t, err, h+"err receiver")

	return sender, recv
}

type seededKem struct {
	seed []byte
	kem.AuthScheme
}

func (a seededKem) Encapsulate(pk kem.PublicKey) (
	ct []byte, ss []byte, err error) {
	return a.AuthScheme.EncapsulateDeterministically(pk, a.seed)
}

func (a seededKem) AuthEncapsulate(pkr kem.PublicKey, sks kem.PrivateKey) (
	ct []byte, ss []byte, err error) {
	if kb, ok := a.AuthScheme.(shortKem); ok {
		return kb.authEncap(pkr, sks, a.seed)
	}
	if kb, ok := a.AuthScheme.(xkem); ok {
		return kb.authEncap(pkr, sks, a.seed)
	}
	panic("bad kem")
}

func (v *vector) setup(t *testing.T, k kem.AuthScheme,
	se *Sender, re *Receiver,
	m modeID, s Suite,
) (Sealer, Opener) {
	h := fmt.Sprintf("mode: %v %v\n", m, s)
	var x func() ([]byte, Sealer, error)
	var y func([]byte) (Opener, error)

	switch v.ModeID {
	case modeBase:
		x = func() ([]byte, Sealer, error) {
			return se.buildBase().allSetup(k)
		}
		y = func(enc []byte) (Opener, error) {
			return re.Setup(enc)
		}
	case modePSK:
		psk, pskid := hexB(v.Psk), hexB(v.PskID)
		x = func() ([]byte, Sealer, error) {
			return se.buildPSK(psk, pskid).allSetup(k)
		}
		y = func(enc []byte) (Opener, error) {
			return re.SetupPSK(enc, psk, pskid)
		}
	case modeAuth:
		x = func() ([]byte, Sealer, error) {
			skS, err := k.UnmarshalBinaryPrivateKey(hexB(v.SkSm))
			test.CheckNoErr(t, err, h+"bad private key")
			se, err = se.buildAuth(skS)
			test.CheckNoErr(t, err, h+"bad private key")
			return se.allSetup(k)
		}
		y = func(enc []byte) (Opener, error) {
			pkS, err := k.UnmarshalBinaryPublicKey(hexB(v.PkSm))
			test.CheckNoErr(t, err, h+"bad public key")
			return re.SetupAuth(enc, pkS)
		}
	case modeAuthPSK:
		psk, pskid := hexB(v.Psk), hexB(v.PskID)
		x = func() ([]byte, Sealer, error) {
			skS, err := k.UnmarshalBinaryPrivateKey(hexB(v.SkSm))
			test.CheckNoErr(t, err, h+"bad private key")
			se, err = se.buildAuthPSK(skS, psk, pskid)
			test.CheckNoErr(t, err, h+"bad private key")
			return se.allSetup(k)
		}
		y = func(enc []byte) (Opener, error) {
			pkS, err := k.UnmarshalBinaryPublicKey(hexB(v.PkSm))
			test.CheckNoErr(t, err, h+"bad public key")
			return re.SetupAuthPSK(enc, psk, pskid, pkS)
		}
	}

	enc, sealer, errS := x()
	test.CheckNoErr(t, errS, h+"error on sender setup")
	opener, errR := y(enc)
	test.CheckNoErr(t, errR, h+"error on receiver setup")

	return sealer, opener
}

func (v *vector) checkAead(t *testing.T, e *encdecCtx, m modeID, s Suite) {
	got := e.baseNonce
	want := hexB(v.BaseNonce)
	if !bytes.Equal(got, want) {
		test.ReportError(t, got, want, m, s)
	}

	got = e.exporterSecret
	want = hexB(v.ExporterSecret)
	if !bytes.Equal(got, want) {
		test.ReportError(t, got, want, m, s)
	}
}

func (v *vector) checkEncryptions(
	t *testing.T,
	se Sealer,
	op Opener,
	m modeID,
	s Suite,
) {
	for j, encv := range v.Encryptions {
		pt := hexB(encv.Plaintext)
		aad := hexB(encv.Aad)

		ct, err := se.Seal(pt, aad)
		test.CheckNoErr(t, err, "error on sealing")

		got, err := op.Open(ct, aad)
		test.CheckNoErr(t, err, "error on opening")

		want := pt
		if !bytes.Equal(got, want) {
			test.ReportError(t, got, want, m, s, j)
		}
	}
}

func (v *vector) checkExports(t *testing.T, exp Exporter, m modeID, s Suite) {
	for j, expv := range v.Exports {
		ctx := hexB(expv.ExportContext)
		want := hexB(expv.ExportValue)

		got := exp.Export(ctx, uint(expv.ExportLength))
		if !bytes.Equal(got, want) {
			test.ReportError(t, got, want, m, s, j)
		}
	}
}

func hexB(x string) []byte { z, _ := hex.DecodeString(x); return z }

func readFile(t *testing.T, fileName string) []vector {
	jsonFile, err := os.Open(fileName)
	if err != nil {
		t.Fatalf("File %v can not be opened. Error: %v", fileName, err)
	}
	defer jsonFile.Close()
	input, _ := ioutil.ReadAll(jsonFile)
	var vectors []vector
	err = json.Unmarshal(input, &vectors)
	if err != nil {
		t.Fatalf("File %v can not be loaded. Error: %v", fileName, err)
	}
	return vectors
}

type vector struct {
	ModeID             uint8  `json:"mode"`
	KemID              uint16 `json:"kem_id"`
	KdfID              uint16 `json:"kdf_id"`
	AeadID             uint16 `json:"aead_id"`
	Info               string `json:"info"`
	IkmR               string `json:"ikmR"`
	IkmE               string `json:"ikmE"`
	SkRm               string `json:"skRm"`
	SkEm               string `json:"skEm"`
	SkSm               string `json:"skSm"`
	Psk                string `json:"psk"`
	PskID              string `json:"psk_id"`
	PkSm               string `json:"pkSm"`
	PkRm               string `json:"pkRm"`
	PkEm               string `json:"pkEm"`
	Enc                string `json:"enc"`
	SharedSecret       string `json:"shared_secret"`
	KeyScheduleContext string `json:"key_schedule_context"`
	Secret             string `json:"secret"`
	Key                string `json:"key"`
	BaseNonce          string `json:"base_nonce"`
	ExporterSecret     string `json:"exporter_secret"`
	Encryptions        []struct {
		Aad        string `json:"aad"`
		Ciphertext string `json:"ciphertext"`
		Nonce      string `json:"nonce"`
		Plaintext  string `json:"plaintext"`
	} `json:"encryptions"`
	Exports []struct {
		ExportContext string `json:"exportContext"`
		ExportLength  int    `json:"exportLength"`
		ExportValue   string `json:"exportValue"`
	} `json:"exports"`
}
