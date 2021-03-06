package stripssl

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"../../helpers"

	"github.com/phuslu/glog"
)

type RootCA struct {
	name     string
	keyFile  string
	certFile string
	rsaBits  int
	certDir  string
	mu       *sync.Mutex

	ca       *x509.Certificate
	priv     *rsa.PrivateKey
	derBytes []byte
}

func NewRootCA(name string, vaildFor time.Duration, rsaBits int, certDir string, portable bool) (*RootCA, error) {
	keyFile := name + ".key"
	certFile := name + ".crt"

	if portable {
		rootdir := filepath.Dir(os.Args[0])
		keyFile = filepath.Join(rootdir, keyFile)
		certFile = filepath.Join(rootdir, certFile)
		certDir = filepath.Join(rootdir, certDir)
	}

	rootCA := &RootCA{
		name:     name,
		keyFile:  keyFile,
		certFile: certFile,
		rsaBits:  rsaBits,
		certDir:  certDir,
		mu:       new(sync.Mutex),
	}

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		glog.Infof("Generating RootCA for %s", certFile)
		template := x509.Certificate{
			IsCA:         true,
			SerialNumber: big.NewInt(1),
			Subject: pkix.Name{
				CommonName:   name,
				Organization: []string{name},
			},
			NotBefore: time.Now().Add(-time.Duration(30 * 24 * time.Hour)),
			NotAfter:  time.Now().Add(vaildFor),

			KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			BasicConstraintsValid: true,
		}

		priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, err
		}

		derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
		if err != nil {
			return nil, err
		}

		ca, err := x509.ParseCertificate(derBytes)
		if err != nil {
			return nil, err
		}

		rootCA.ca = ca
		rootCA.priv = priv
		rootCA.derBytes = derBytes

		keypem := &pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rootCA.priv)}
		if err = ioutil.WriteFile(keyFile, pem.EncodeToMemory(keypem), 0755); err != nil {
			return nil, err
		}

		certpem := &pem.Block{Type: "CERTIFICATE", Bytes: rootCA.derBytes}
		if err = ioutil.WriteFile(certFile, pem.EncodeToMemory(certpem), 0755); err != nil {
			return nil, err
		}
	} else {
		data, err := ioutil.ReadFile(keyFile)
		if err != nil {
			return nil, err
		}

		var b *pem.Block
		for {
			b, data = pem.Decode(data)
			if b == nil {
				break
			}
			if b.Type == "CERTIFICATE" {
				rootCA.derBytes = b.Bytes
				ca, err := x509.ParseCertificate(rootCA.derBytes)
				if err != nil {
					return nil, err
				}
				rootCA.ca = ca
			} else if b.Type == "PRIVATE KEY" {
				priv, err := x509.ParsePKCS1PrivateKey(b.Bytes)
				if err != nil {
					return nil, err
				}
				rootCA.priv = priv
			}
		}

		data, err = ioutil.ReadFile(certFile)
		if err != nil {
			return nil, err
		}

		for {
			b, data = pem.Decode(data)
			if b == nil {
				break
			}
			if b.Type == "CERTIFICATE" {
				rootCA.derBytes = b.Bytes
				ca, err := x509.ParseCertificate(rootCA.derBytes)
				if err != nil {
					return nil, err
				}
				rootCA.ca = ca
			} else if b.Type == "PRIVATE KEY" {
				priv, err := x509.ParsePKCS1PrivateKey(b.Bytes)
				if err != nil {
					return nil, err
				}
				rootCA.priv = priv
			}
		}
	}

	switch runtime.GOOS {
	case "windows", "darwin":
		if _, err := rootCA.ca.Verify(x509.VerifyOptions{}); err != nil {
			glog.Warningf("Verify RootCA(%#v) error: %v, try import to system root", name, err)
			if err = helpers.RemoveCAFromSystemRoot(rootCA.name); err != nil {
				glog.Errorf("Remove Old RootCA(%#v) error: %v", name, err)
			}
			if err = helpers.ImportCAToSystemRoot(rootCA.ca); err != nil {
				glog.Errorf("Import RootCA(%#v) error: %v", name, err)
			} else {
				glog.Infof("Import RootCA(%s) OK", certFile)
			}

			if fis, err := ioutil.ReadDir(certDir); err == nil {
				for _, fi := range fis {
					if err = os.Remove(certDir + "/" + fi.Name()); err != nil {
						glog.Errorf("Remove(%#v) error: %v", fi.Name(), err)
					}
				}
			}
		}
	}

	if _, err := os.Stat(certDir); os.IsNotExist(err) {
		if err = os.Mkdir(certDir, 0755); err != nil {
			return nil, err
		}
	}

	return rootCA, nil
}

func (c *RootCA) issue(commonName string, vaildFor time.Duration, rsaBits int) error {
	certFile := c.toFilename(commonName, ".crt")

	csrTemplate := &x509.CertificateRequest{
		Signature: []byte(commonName),
		Subject: pkix.Name{
			Country:            []string{"CN"},
			Organization:       []string{commonName},
			OrganizationalUnit: []string{c.name},
			CommonName:         commonName,
		},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}

	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return err
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, priv)
	if err != nil {
		return err
	}

	csr, err := x509.ParseCertificateRequest(csrBytes)
	if err != nil {
		return err
	}

	certTemplate := &x509.Certificate{
		Subject:            csr.Subject,
		PublicKeyAlgorithm: csr.PublicKeyAlgorithm,
		PublicKey:          csr.PublicKey,
		SerialNumber:       big.NewInt(time.Now().UnixNano()),
		SignatureAlgorithm: x509.SHA256WithRSA,
		NotBefore:          time.Now().Add(-time.Duration(30 * 24 * time.Hour)),
		NotAfter:           time.Now().Add(vaildFor),
		KeyUsage:           x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, c.ca, csr.PublicKey, c.priv)
	if err != nil {
		return err
	}

	outFile, err := os.Create(certFile)
	defer outFile.Close()
	if err != nil {
		return err
	}
	pem.Encode(outFile, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	pem.Encode(outFile, &pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return nil
}

func GetCommonName(domain string) string {
	parts := strings.Split(domain, ".")
	switch len(parts) {
	case 1, 2:
		break
	case 3:
		len1 := len(parts[len(parts)-1])
		len2 := len(parts[len(parts)-2])
		switch {
		case len1 >= 3 || len2 >= 4:
			domain = "*." + strings.Join(parts[1:], ".")
		}
	default:
		domain = "*." + strings.Join(parts[1:], ".")
	}
	return domain
}

func (c *RootCA) RsaBits() int {
	return c.rsaBits
}

func (c *RootCA) toFilename(commonName, suffix string) string {
	if strings.HasPrefix(commonName, "*.") {
		commonName = commonName[1:]
	}
	return c.certDir + "/" + commonName + suffix
}

func (c *RootCA) Issue(commonName string, vaildFor time.Duration, rsaBits int) (*tls.Certificate, error) {
	certFile := c.toFilename(commonName, ".crt")

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		glog.V(2).Infof("Issue %s certificate for %#v...", c.name, commonName)
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, err := os.Stat(certFile); os.IsNotExist(err) {
			if err = c.issue(commonName, vaildFor, rsaBits); err != nil {
				return nil, err
			}
		}
	}

	tlsCert, err := tls.LoadX509KeyPair(certFile, certFile)
	if err != nil {
		return nil, err
	}
	return &tlsCert, nil
}
