package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"
)

// bulkIntegrityBytes is large enough to span thousands of relay frames and to
// outrun every flow-control window, like the 29 MB rz upload that shows garbage
// in a JumpServer terminal.
const bulkIntegrityBytes = 24 << 20

// prngStream is a reproducible pseudo-random byte stream. Unlike a repeated
// byte, a lost, duplicated or reordered frame changes its digest.
func prngStream(seed, length int64) io.Reader {
	return io.LimitReader(rand.New(rand.NewSource(seed)), length)
}

func digestOf(seed, length int64) []byte {
	sum := sha256.New()
	_, _ = io.Copy(sum, prngStream(seed, length))
	return sum.Sum(nil)
}

const (
	integrityUploadSeed   = 11
	integrityDownloadSeed = 22
)

// startIntegrityTarget models a host running both `rz` and `sz` at once: it
// reads an uploaded stream while streaming its own data back, then reports the
// digest of what it received. The upload length is a header rather than EOF so
// the check does not depend on half-close propagation.
func startIntegrityTarget() (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveIntegrity(conn)
		}
	}()
	return listener, nil
}

func serveIntegrity(conn net.Conn) {
	defer conn.Close()
	var header [16]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	uploadLength := int64(binary.BigEndian.Uint64(header[:8]))
	downloadLength := int64(binary.BigEndian.Uint64(header[8:]))
	downloaded := make(chan struct{})
	go func() {
		defer close(downloaded)
		_, _ = io.Copy(conn, prngStream(integrityDownloadSeed, downloadLength))
	}()
	received := sha256.New()
	if _, err := io.CopyN(received, conn, uploadLength); err != nil {
		return
	}
	<-downloaded
	_, _ = conn.Write(received.Sum(nil))
}

func TestSOCKS5BulkIntegrity(t *testing.T) {
	fixture, err := newLatencyFixture(t)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close(t)

	paths := []struct {
		name    string
		address func() string
	}{
		{"local relay", func() string { return fixture.socksA.Addr().String() }},
		{"cross-node relay", func() string { return fixture.socksB.Addr().String() }},
	}
	directions := []struct {
		name             string
		upload, download int64
	}{
		{"upload only", bulkIntegrityBytes, 0},
		{"download only", 0, bulkIntegrityBytes},
		{"both at once", bulkIntegrityBytes, bulkIntegrityBytes},
	}
	for _, path := range paths {
		for _, direction := range directions {
			t.Run(path.name+"/"+direction.name, func(t *testing.T) {
				checkBulkIntegrity(t, path.address(), fixture.integrityPort, direction.upload, direction.download)
			})
		}
	}
}

func checkBulkIntegrity(t *testing.T, address string, port int, upload, download int64) {
	t.Helper()
	conn, reply := socks5Connect(t, address, "127.0.0.1", port)
	if reply != 0 {
		t.Fatalf("SOCKS5 reply=%d, want success", reply)
	}
	defer conn.Close()
	// socks5Connect leaves a short handshake deadline behind.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], uint64(upload))
	binary.BigEndian.PutUint64(header[8:], uint64(download))
	uploadErr := make(chan error, 1)
	go func() {
		if _, err := conn.Write(header[:]); err != nil {
			uploadErr <- err
			return
		}
		_, err := io.Copy(conn, prngStream(integrityUploadSeed, upload))
		uploadErr <- err
	}()

	var downloaded hash.Hash = sha256.New()
	if n, err := io.CopyN(downloaded, conn, download); err != nil {
		t.Fatalf("download stopped after %d of %d bytes: %v", n, download, err)
	}
	if !bytes.Equal(downloaded.Sum(nil), digestOf(integrityDownloadSeed, download)) {
		t.Fatal("downloaded bytes differ from what the target sent")
	}
	if err := <-uploadErr; err != nil {
		t.Fatalf("upload of %d bytes did not finish: %v", upload, err)
	}
	remoteDigest := make([]byte, sha256.Size)
	if _, err := io.ReadFull(conn, remoteDigest); err != nil {
		t.Fatalf("read the target's digest of the upload: %v", err)
	}
	if !bytes.Equal(remoteDigest, digestOf(integrityUploadSeed, upload)) {
		t.Fatal("the target received different bytes than the client uploaded")
	}
}
