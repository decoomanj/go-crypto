// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package packet

import (
	"bytes"
	"testing"
	"time"
)

var privateKeyTests = []struct {
	privateKeyHex string
	creationTime  time.Time
}{
	{
		privKeyRSAHex,
		time.Unix(0x4cc349a8, 0),
	},
	{
		privKeyElGamalHex,
		time.Unix(0x4df9ee1a, 0),
	},
}

var message = []byte("This is a test")
var oldPassphrase = []byte("testing")
var newPassphrase = []byte("try Me instead")

func TestPrivateKeyRead(t *testing.T) {
	for i, test := range privateKeyTests {
		packet, err := Read(readerFromHex(test.privateKeyHex))
		if err != nil {
			t.Errorf("#%d: failed to parse: %s", i, err)
			continue
		}

		privKey := packet.(*PrivateKey)

		if !privKey.Encrypted {
			t.Errorf("#%d: private key isn't encrypted", i)
			continue
		}

		err = privKey.Decrypt([]byte("wrong password"))
		if err == nil {
			t.Errorf("#%d: decrypted with incorrect key", i)
			continue
		}

		err = privKey.Decrypt([]byte("testing"))
		if err != nil {
			t.Errorf("#%d: failed to decrypt: %s", i, err)
			continue
		}

		if !privKey.CreationTime.Equal(test.creationTime) || privKey.Encrypted {
			t.Errorf("#%d: bad result, got: %#v", i, privKey)
		}
	}
}

// This test the following 2 items:
// * re-encrypt the PrivateKey with a new passphrase and can decrypt it
//   with this new passphrase
// * serialize and deserialized the PrivateKey with the changed passphrase
// The steps are:
// 1. Re-encrypt PrivateKey with new passphrase
// 2. Serialize the PrivateKey
// 3. Read the new serialized form into a new PrivateKey
// 4. Verify the we can decrypt the PrivateKey with the new passphrase and
//    not the old one.
func TestPrivateKeyEncrypt(t *testing.T) {
	for i, test := range privateKeyTests {
		packet, err := Read(readerFromHex(test.privateKeyHex))
		if err != nil {
			t.Errorf("#%d: failed to parse: %s", i, err)
			continue
		}

		privKey := packet.(*PrivateKey)
		if err = privKey.Decrypt(oldPassphrase); err != nil {
			t.Errorf("#%d: failed to decrypt: %s", i, err)
			continue
		}

		privKey.Encrypt(newPassphrase, nil)
		privKeyBuf := bytes.NewBuffer(nil)
		if err = privKey.Serialize(privKeyBuf); err != nil {
			t.Errorf("#%d: failed to serialize: %s", i, err)
			continue
		}

		// Now load the serialized form into a new PrivateKey
		var packet2 Packet
		if packet2, err = Read(privKeyBuf); err != nil {
			t.Errorf("#%d: failed to parse: %s", i, err)
		}

		pKey2 := packet2.(*PrivateKey)
		if err = pKey2.Decrypt(oldPassphrase); err == nil {
			t.Errorf("#%d: decrypted with the old passphrase!", i)
			continue
		}
		if err = pKey2.Decrypt(newPassphrase); err != nil {
			t.Errorf("#%d: failed to decrypt with new passphrase: %s", i, err)
			continue
		}
	}
}

func TestIssue11505(t *testing.T) {
	// parsing a rsa private key with p or q == 1 used to panic due to a divide by zero
	_, _ = Read(readerFromHex("9c3004303030300100000011303030000000000000010130303030303030303030303030303030303030303030303030303030303030303030303030303030303030"))
}

// Generated with `gpg --export-secret-keys "Test Key 2"`
const privKeyRSAHex = "9501fe044cc349a8010400b70ca0010e98c090008d45d1ee8f9113bd5861fd57b88bacb7c68658747663f1e1a3b5a98f32fda6472373c024b97359cd2efc88ff60f77751adfbf6af5e615e6a1408cfad8bf0cea30b0d5f53aa27ad59089ba9b15b7ebc2777a25d7b436144027e3bcd203909f147d0e332b240cf63d3395f5dfe0df0a6c04e8655af7eacdf0011010001fe0303024a252e7d475fd445607de39a265472aa74a9320ba2dac395faa687e9e0336aeb7e9a7397e511b5afd9dc84557c80ac0f3d4d7bfec5ae16f20d41c8c84a04552a33870b930420e230e179564f6d19bb153145e76c33ae993886c388832b0fa042ddda7f133924f3854481533e0ede31d51278c0519b29abc3bf53da673e13e3e1214b52413d179d7f66deee35cac8eacb060f78379d70ef4af8607e68131ff529439668fc39c9ce6dfef8a5ac234d234802cbfb749a26107db26406213ae5c06d4673253a3cbee1fcbae58d6ab77e38d6e2c0e7c6317c48e054edadb5a40d0d48acb44643d998139a8a66bb820be1f3f80185bc777d14b5954b60effe2448a036d565c6bc0b915fcea518acdd20ab07bc1529f561c58cd044f723109b93f6fd99f876ff891d64306b5d08f48bab59f38695e9109c4dec34013ba3153488ce070268381ba923ee1eb77125b36afcb4347ec3478c8f2735b06ef17351d872e577fa95d0c397c88c71b59629a36aec"

// Generated by `gpg --export-secret-keys` followed by a manual extraction of
// the ElGamal subkey from the packets.
const privKeyElGamalHex = "9d0157044df9ee1a100400eb8e136a58ec39b582629cdadf830bc64e0a94ed8103ca8bb247b27b11b46d1d25297ef4bcc3071785ba0c0bedfe89eabc5287fcc0edf81ab5896c1c8e4b20d27d79813c7aede75320b33eaeeaa586edc00fd1036c10133e6ba0ff277245d0d59d04b2b3421b7244aca5f4a8d870c6f1c1fbff9e1c26699a860b9504f35ca1d700030503fd1ededd3b840795be6d9ccbe3c51ee42e2f39233c432b831ddd9c4e72b7025a819317e47bf94f9ee316d7273b05d5fcf2999c3a681f519b1234bbfa6d359b4752bd9c3f77d6b6456cde152464763414ca130f4e91d91041432f90620fec0e6d6b5116076c2985d5aeaae13be492b9b329efcaf7ee25120159a0a30cd976b42d7afe030302dae7eb80db744d4960c4df930d57e87fe81412eaace9f900e6c839817a614ddb75ba6603b9417c33ea7b6c93967dfa2bcff3fa3c74a5ce2c962db65b03aece14c96cbd0038fc"
