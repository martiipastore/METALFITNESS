// Package migracion calcula y compara las huellas de las migraciones SQL
// versionadas. Es técnico: no conoce tablas ni reglas de ningún servicio.
package migracion

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// Huella es el SHA-256 del contenido con los finales de línea normalizados a LF.
// Así el mismo archivo da la misma huella en Windows y en Linux, sin importar
// cómo lo haya escrito Git al hacer checkout.
func Huella(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(lf(data)))
}

// Coincide indica si la huella guardada al aplicar la migración corresponde al
// contenido actual. Acepta la huella normalizada y las que se guardaban antes de
// normalizar: la del archivo con LF, con CRLF o tal como estaba en disco. Un
// cambio real del contenido sigue detectándose.
func Coincide(guardada string, data []byte) bool {
	normal := lf(data)
	for _, variante := range [][]byte{normal, bytes.ReplaceAll(normal, []byte("\n"), []byte("\r\n")), data} {
		if fmt.Sprintf("%x", sha256.Sum256(variante)) == guardada {
			return true
		}
	}
	return false
}

func lf(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}
