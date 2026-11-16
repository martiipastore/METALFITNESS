package migracion

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func huellaCruda(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// Una base aplicada con el archivo en CRLF (checkout en Windows) o en LF (Linux,
// CI) acepta el mismo archivo con cualquiera de los dos finales de línea.
func TestCoincideConLFYCRLF(t *testing.T) {
	lfSQL := []byte("CREATE TABLE x (\n  id int\n);\n")
	crlfSQL := []byte("CREATE TABLE x (\r\n  id int\r\n);\r\n")
	if Huella(lfSQL) != Huella(crlfSQL) {
		t.Fatal("la huella no depende del final de línea")
	}
	for _, guardada := range []string{huellaCruda(lfSQL), huellaCruda(crlfSQL), Huella(crlfSQL)} {
		for _, actual := range [][]byte{lfSQL, crlfSQL} {
			if !Coincide(guardada, actual) {
				t.Errorf("la huella %s.. debería aceptar el mismo contenido con otro final de línea", guardada[:8])
			}
		}
	}
}

func TestDetectaUnCambioReal(t *testing.T) {
	original := []byte("CREATE TABLE x (id int);\n")
	cambiado := []byte("CREATE TABLE x (id bigint);\n")
	if Coincide(Huella(original), cambiado) || Coincide(huellaCruda(original), cambiado) {
		t.Fatal("un cambio de contenido debe rechazarse")
	}
}
