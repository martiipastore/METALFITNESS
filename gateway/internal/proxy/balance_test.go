// Balanceo por el proxy (etapa 12, parte B; ADR-012 v3): las solicitudes se
// alternan entre instancias y, sin ninguna elegible, el gateway responde 503 sin
// intentar un destino caído. Se usa la API de partners porque la autentica el
// servicio, no el gateway; el mecanismo es el mismo para Booking.
package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/gateway/internal/balance"
	gwconfig "github.com/GuadiViti/Arquitectura-de-Software/gateway/internal/config"
)

func TestProxyBalanceaEntreInstancias(t *testing.T) {
	var a, b atomic.Int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { a.Add(1) }))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { b.Add(1) }))
	defer srvB.Close()

	cfg, err := gwconfig.LoadFromMap(map[string]string{"GATEWAY_BENEFITS_URL": srvA.URL + ", " + srvB.URL})
	require.NoError(t, err)
	svc, ok := cfg.Service(gwconfig.Benefits)
	require.True(t, ok)
	require.Len(t, svc.URLs, 2)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := New(cfg, nil, log)
	require.NoError(t, err)
	bal := balance.New(gwconfig.Benefits, svc.URLs, balance.PoliticaAprobada, http.DefaultClient, log)
	p.WithBalanceadores(map[string]*balance.Balanceador{gwconfig.Benefits: bal})

	pedir := func() int {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partner-api/v2/accounts/cli-1/balance", nil))
		return rec.Code
	}
	for k := 0; k < 10; k++ {
		require.Equal(t, 200, pedir())
	}
	require.EqualValues(t, 5, a.Load(), "mitad a la instancia 1")
	require.EqualValues(t, 5, b.Load(), "mitad a la instancia 2")
	require.Contains(t, p.MetricasBalanceo(), `gateway_balancer_requests_total{service="benefits-service",instancia="benefits-service-1"} 5`)

	for _, i := range bal.Instancias {
		for k := 0; k < 3; k++ {
			bal.Registrar(i, false)
		}
	}
	antes := a.Load() + b.Load()
	require.Equal(t, http.StatusBadGateway, pedir(), "sin instancias elegibles: 502 UNAVAILABLE del contrato de partners")
	require.Equal(t, antes, a.Load()+b.Load(), "no se intenta un destino retirado")
	require.Contains(t, p.MetricasBalanceo(), `gateway_balancer_instance_up{service="benefits-service",instancia="benefits-service-2"} 0`)
}
