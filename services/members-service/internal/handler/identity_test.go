// HU-CB-02; RN-CB-03/11/13; ADR-006: el actor proviene del gateway.
package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCuentaPrivadaExigeActorConCredencialInterna(t *testing.T) {
	secret := strings.Repeat("s", 32)
	r := gin.New()
	// Un servicio nil permite detectar si un rechazo llega al caso de uso.
	RegisterIdentity(r, nil, secret)
	for _, tc := range []struct {
		name, method, path string
		headers            map[string]string
		duplicate          bool
	}{
		{"sin_actor", "GET", "/api/v1/users/me", nil, false},
		{"JWT_directo_no_es_actor", "GET", "/api/v1/users/me", map[string]string{"Authorization": "Bearer no-validar-en-servicio"}, false},
		{"solo_clave", "GET", "/api/v1/users/me", map[string]string{"X-Internal-Service-Key": secret}, false},
		{"rol_inventado", "GET", "/api/v1/users/me", map[string]string{"X-Internal-Service-Key": secret, "X-User-Id": "11111111-1111-4111-8111-111111111111", "X-User-Role": "admin"}, false},
		{"clave_duplicada", "GET", "/api/v1/users/me", map[string]string{"X-Internal-Service-Key": secret, "X-User-Id": "11111111-1111-4111-8111-111111111111", "X-User-Role": "ALUMNO"}, true},
		{"logout_sin_actor", "POST", "/api/v1/auth/logout", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			if tc.duplicate {
				req.Header.Add("X-Internal-Service-Key", secret)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 401, w.Code)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		})
	}
}

// Jornada de fallas del 15/11/2026: con PostgreSQL caído el login respondía 500.
// Un error sin clasificar es una dependencia que falló: 503 con Retry-After.
func TestErrorSinClasificarEsDependenciaNoDisponible(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	identityError(c, errors.New("failed to connect to host=postgres"))
	require.Equal(t, 503, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "DEPENDENCIA_NO_DISPONIBLE")
	require.NotContains(t, w.Body.String(), "postgres", "nunca se expone el error interno")
}
