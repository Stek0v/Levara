package storage

import "testing"

func TestDestinationIdentityBindsNamespaceAndIgnoresKeyRotation(t *testing.T) {
	a := &S3Storage{endpoint: "https://objects.example.test", region: "test", bucket: "a"}
	id, err := DestinationIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	a.stateKey[0]++
	if same, _ := DestinationIdentity(a); same != id {
		t.Fatal("credential/state rotation changed destination")
	}
	a.bucket = "b"
	if other, _ := DestinationIdentity(a); other == id {
		t.Fatal("bucket change kept destination")
	}
	e := &EncryptedStorage{inner: a, writeKey: "old"}
	encrypted, err := DestinationIdentity(e)
	if err != nil {
		t.Fatal(err)
	}
	e.writeKey = "new"
	if same, _ := DestinationIdentity(e); same != encrypted {
		t.Fatal("write key rotation changed destination")
	}
	if plain, _ := DestinationIdentity(a); plain == encrypted {
		t.Fatal("plaintext and envelope destinations collided")
	}
	local := NewLocalStorage(t.TempDir())
	if _, err := DestinationIdentity(local); err != nil {
		t.Fatal(err)
	}
}
