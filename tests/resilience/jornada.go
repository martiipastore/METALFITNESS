//go:build ignore

// Jornada de fallas controladas de la entrega final (enunciado §2.9 y §3,
// docs/POSTMORTEM.md). Con el stack de `make demo` arriba, detiene una por vez
// dependencias propias mientras corre tráfico liviano y continuo, y registra qué
// respondió el sistema, qué detectaron el estado agregado y las alertas, y cuánto
// tardó en recuperarse. La caída del proveedor externo (MELT) se suma cuando el
// grupo proveedor entregue su corrección.
//
//	go run tests/resilience/jornada.go            # unos 15 minutos
//	go run tests/resilience/jornada.go -corta     # una sola falla, para probar
//
// Usa los alumnos sintéticos de la carga (carga.NNN@gym.local): correr antes una
// vez `go run tests/load/run-flujo.go -corta`. Las contraseñas se derivan de
// GYM_INTERNAL_SERVICE_KEY y no se imprimen. Al terminar, o si falla, vuelve a
// iniciar todos los contenedores que detuvo.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	gateway    = "http://localhost:8080"
	prometheus = "http://127.0.0.1:9090"
)

var (
	corta  = flag.Bool("corta", false, "solo la caída de OpenSearch, con ventanas cortas")
	salida = flag.String("salida", "docs/evidence/entrega-final/jornada", "carpeta de evidencia")
	env    map[string]string
	client = &http.Client{Timeout: 6 * time.Second}
	// detenidos son los contenedores que hay que volver a iniciar si la jornada
	// termina antes de tiempo (os.Exit no ejecuta los defer).
	detenidos = map[string]bool{}
)

// falla describe una caída provocada y lo que el diseño dice que debe ocurrir.
type falla struct {
	Nombre     string `json:"nombre"`
	Contenedor string `json:"contenedor"`
	Esperado   string `json:"esperado"`
	Diseño     string `json:"diseno"`
}

var fallas = []falla{
	{"OpenSearch caído", "gym-opensearch-1", "Búsqueda servida desde la caché (fresca o vencida); reservas normales; clase.actualizada espera en RabbitMQ", "ADR-014, ADR-007 v2, D-95"},
	{"PostgreSQL caído", "gym-postgres-1", "Búsqueda con la copia de Redis y marcas_disponibles=false; reservar y login fallan cerrados con 503", "ADR-007 v2, D-95, ADR-005 v2"},
	{"RabbitMQ caído", "gym-rabbitmq-1", "Reservas y búsqueda normales; el outbox acumula y se vacía al volver el broker", "ADR-005 v2, regla 6"},
	{"Redis caído", "gym-redis-1", "Login falla cerrado (503); el resto sigue sin límite de tráfico; la búsqueda va a la fuente", "D-91, ADR-007"},
	{"Members caído", "gym-members-service-1", "Reservar falla cerrado (503); búsqueda normal; login no disponible", "ADR-005 v2, ADR-007"},
}

// muestra es una solicitud del tráfico de fondo.
type muestra struct {
	T       time.Time `json:"t"`
	Op      string    `json:"op"`
	Status  int       `json:"status"`
	Code    string    `json:"code,omitempty"`
	Origen  string    `json:"origen,omitempty"`
	Marcas  *bool     `json:"marcas,omitempty"`
	Latency float64   `json:"latencia_ms"`
}

type estado struct {
	T         time.Time         `json:"t"`
	Global    string            `json:"global"`
	Servicios map[string]string `json:"servicios"`
}

type alerta struct {
	T      time.Time `json:"t"`
	Nombre string    `json:"nombre"`
	Estado string    `json:"estado"`
	Labels string    `json:"labels"`
}

type registro struct {
	mu       sync.Mutex
	muestras []muestra
	estados  []estado
	alertas  []alerta
	outbox   []map[string]any
	token    string
}

func (r *registro) agregar(m muestra) { r.mu.Lock(); r.muestras = append(r.muestras, m); r.mu.Unlock() }

// resultado resume una falla para el postmortem.
type resultado struct {
	Falla           falla                     `json:"falla"`
	Detenida        time.Time                 `json:"detenida"`
	Iniciada        time.Time                 `json:"iniciada"`
	Lista           *time.Time                `json:"lista,omitempty"`
	RecuperacionS   float64                   `json:"recuperacion_s"`
	PorOperacion    map[string]map[string]int `json:"por_operacion"`
	OrigenBusqueda  map[string]int            `json:"origen_busqueda"`
	SinMarcas       int                       `json:"busquedas_sin_marcas"`
	PrimeraFalla    *time.Time                `json:"primera_falla,omitempty"`
	DeteccionEstado *time.Time                `json:"deteccion_estado,omitempty"`
	Alertas         []alerta                  `json:"alertas"`
	OutboxMax       int                       `json:"outbox_pendiente_max"`
	OutboxFinal     int                       `json:"outbox_pendiente_final"`
	LogsError       map[string]int            `json:"logs_warn_error"`
}

func main() {
	flag.Parse()
	env = leerEnv(".env")
	must(os.MkdirAll(*salida, 0o755), "carpeta de evidencia")
	plan, normal, caida := fallas, 30*time.Second, 60*time.Second
	if *corta {
		plan, normal, caida = fallas[:1], 10*time.Second, 20*time.Second
	}
	defer reiniciarDetenidos()

	reg := &registro{}
	alumno := "carga.001@gym.local"
	reg.token = login(alumno)
	if reg.token == "" {
		fatal("no inicia sesión carga.001@gym.local: correr antes go run tests/load/run-flujo.go -corta")
	}
	claseID := claseParaReservar(reg.token)
	paso("Jornada de fallas controladas: tráfico de fondo con un alumno (búsqueda cada 0,5 s, reserva y cancelación cada 4 s, login cada 15 s)")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	cada := func(d time.Duration, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(d)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					f()
				}
			}
		}()
	}
	textos := []string{"", "zum", "fun", "pil"}
	cada(500*time.Millisecond, func() { buscar(reg, textos[rand.Intn(len(textos))]) })
	cada(4*time.Second, func() { reservarYCancelar(reg, claseID) })
	cada(15*time.Second, func() {
		inicio := time.Now()
		if t := login(alumno); t != "" {
			reg.mu.Lock()
			reg.token = t
			reg.mu.Unlock()
			reg.agregar(muestra{T: inicio, Op: "login", Status: 200, Latency: ms(inicio)})
		} else {
			reg.agregar(muestra{T: inicio, Op: "login", Status: ultimoLogin.status, Code: ultimoLogin.code, Latency: ms(inicio)})
		}
	})
	cada(2*time.Second, func() { reg.mu.Lock(); reg.estados = append(reg.estados, consultarEstado()); reg.mu.Unlock() })
	vistas := map[string]string{}
	cada(5*time.Second, func() {
		for _, a := range consultarAlertas() {
			clave := a.Nombre + a.Labels
			if vistas[clave] != a.Estado {
				vistas[clave] = a.Estado
				reg.mu.Lock()
				reg.alertas = append(reg.alertas, a)
				reg.mu.Unlock()
			}
		}
	})
	cada(5*time.Second, func() {
		if n, ok := outboxPendiente(); ok {
			reg.mu.Lock()
			reg.outbox = append(reg.outbox, map[string]any{"t": time.Now(), "pendientes": n})
			reg.mu.Unlock()
		}
	})

	time.Sleep(normal)
	var resultados []resultado
	for _, f := range plan {
		paso(fmt.Sprintf("%s: docker stop %s durante %s", f.Nombre, f.Contenedor, caida))
		r := resultado{Falla: f}
		r.Detenida = time.Now()
		detenidos[f.Contenedor] = true
		must(exec.Command("docker", "stop", f.Contenedor).Run(), "detener "+f.Contenedor)
		time.Sleep(caida)
		r.Iniciada = time.Now()
		must(exec.Command("docker", "start", f.Contenedor).Run(), "iniciar "+f.Contenedor)
		delete(detenidos, f.Contenedor)
		limite := time.Now().Add(4 * time.Minute)
		for time.Now().Before(limite) {
			if e := consultarEstado(); e.Global == "ready" {
				t := time.Now()
				r.Lista = &t
				r.RecuperacionS = t.Sub(r.Iniciada).Seconds()
				break
			}
			time.Sleep(time.Second)
		}
		if r.Lista == nil {
			fmt.Println("  aviso: el sistema no volvió a ready en 4 minutos")
		}
		time.Sleep(normal) // observar la recuperación y vaciado del outbox
		resultados = append(resultados, resumir(reg, r))
		imprimir(resultados[len(resultados)-1])
	}
	close(stop)
	wg.Wait()

	reg.mu.Lock()
	defer reg.mu.Unlock()
	escribirJSON("muestras.json", reg.muestras)
	escribirJSON("estados.json", reg.estados)
	escribirJSON("alertas.json", reg.alertas)
	escribirJSON("outbox.json", reg.outbox)
	escribirJSON("resultados.json", resultados)
	informe(resultados)
	fmt.Println("\nEvidencia en", *salida)
}

// ---- Tráfico de fondo ----

func buscar(reg *registro, q string) {
	reg.mu.Lock()
	token := reg.token
	reg.mu.Unlock()
	path := "/api/v1/classes?pagina=1&tamano=10"
	if q != "" {
		path += "&q=" + q
	}
	inicio := time.Now()
	status, body := pedir(token, http.MethodGet, path, nil, "")
	m := muestra{T: inicio, Op: "busqueda", Status: status, Latency: ms(inicio)}
	var p struct {
		Origen string `json:"origen"`
		Marcas *bool  `json:"marcas_disponibles"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	m.Origen, m.Marcas, m.Code = p.Origen, p.Marcas, p.Code
	reg.agregar(m)
}

func reservarYCancelar(reg *registro, claseID string) {
	reg.mu.Lock()
	token := reg.token
	reg.mu.Unlock()
	inicio := time.Now()
	status, body := pedir(token, http.MethodPost, "/api/v1/bookings", map[string]string{"clase_id": claseID}, fmt.Sprintf("jornada-%d", inicio.UnixNano()))
	var out struct {
		Code    string `json:"code"`
		Reserva struct {
			ID string `json:"id"`
		} `json:"reserva"`
	}
	_ = json.Unmarshal(body, &out)
	reg.agregar(muestra{T: inicio, Op: "reserva", Status: status, Code: out.Code, Latency: ms(inicio)})
	if status == 201 {
		inicio = time.Now()
		st, b := pedir(token, http.MethodPost, "/api/v1/bookings/"+out.Reserva.ID+"/cancel", nil, fmt.Sprintf("jornada-c-%d", inicio.UnixNano()))
		var c struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &c)
		reg.agregar(muestra{T: inicio, Op: "cancelacion", Status: st, Code: c.Code, Latency: ms(inicio)})
	}
}

// claseParaReservar elige una clase futura con cupo, a más de 3 horas, que el
// alumno no tenga reservada (la jornada reserva y cancela siempre la misma).
func claseParaReservar(token string) string {
	hoy := time.Now()
	path := fmt.Sprintf("/api/v1/classes?fecha_desde=%s&fecha_hasta=%s&solo_disponibles=true&pagina=1&tamano=100", hoy.Format(time.DateOnly), hoy.AddDate(0, 0, 13).Format(time.DateOnly))
	// Recién levantado el stack, la primera consulta a OpenSearch puede superar
	// el timeout: se reintenta hasta un minuto antes de empezar.
	var status int
	var body []byte
	for i := 0; i < 30; i++ {
		if status, body = pedir(token, http.MethodGet, path, nil, ""); status == 200 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if status != 200 {
		fatal(fmt.Sprintf("no se pudieron listar clases: HTTP %d", status))
	}
	var p struct {
		Items []struct {
			ID        string    `json:"id"`
			Inicio    time.Time `json:"inicio"`
			Reservada bool      `json:"reservada_por_mi"`
			Actividad struct {
				Nombre string `json:"nombre"`
			} `json:"actividad"`
		} `json:"items"`
	}
	_ = json.Unmarshal(body, &p)
	for _, c := range p.Items {
		if c.Inicio.After(hoy.Add(3*time.Hour)) && !c.Reservada && c.Actividad.Nombre != "Musculación" {
			return c.ID
		}
	}
	fatal("no hay clases futuras con cupo: correr antes go run scripts/cargar-datos-demo.go")
	return ""
}

// ---- Observación ----

func consultarEstado() estado {
	e := estado{T: time.Now(), Global: "sin_respuesta", Servicios: map[string]string{}}
	res, err := client.Get(gateway + "/api/v1/status")
	if err != nil {
		return e
	}
	defer res.Body.Close()
	var s struct {
		Status   string `json:"status"`
		Services []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"services"`
	}
	if json.NewDecoder(res.Body).Decode(&s) == nil {
		e.Global = s.Status
		for _, x := range s.Services {
			e.Servicios[x.Name] = x.Status
		}
	}
	return e
}

func consultarAlertas() []alerta {
	res, err := client.Get(prometheus + "/api/v1/alerts")
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
				State  string            `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil {
		return nil
	}
	var as []alerta
	for _, a := range out.Data.Alerts {
		var l []string
		for k, v := range a.Labels {
			if k != "alertname" && k != "severity" {
				l = append(l, k+"="+v)
			}
		}
		sort.Strings(l)
		as = append(as, alerta{T: time.Now(), Nombre: a.Labels["alertname"], Estado: a.State, Labels: strings.Join(l, ",")})
	}
	return as
}

func outboxPendiente() (int, bool) {
	out, err := exec.Command("docker", "exec", "gym-postgres-1", "psql", "-U", "postgres", "-d", "booking_db", "-tAc",
		"SELECT count(*) FROM outbox WHERE publicado_en IS NULL").Output()
	if err != nil {
		return 0, false
	}
	var n int
	_, err = fmt.Sscan(strings.TrimSpace(string(out)), &n)
	return n, err == nil
}

// logsConProblemas cuenta, por contenedor, las líneas WARN y ERROR de la
// ventana. Solo cuenta: no copia mensajes (pueden tener identificadores).
func logsConProblemas(desde, hasta time.Time) map[string]int {
	out := map[string]int{}
	for _, c := range []string{"gym-api-gateway-1", "gym-booking-service-1", "gym-booking-service-2-1", "gym-members-service-1", "gym-booking-indexer-1", "gym-benefits-service-1"} {
		b, err := exec.Command("docker", "logs", "--since", desde.UTC().Format(time.RFC3339), "--until", hasta.UTC().Format(time.RFC3339), c).CombinedOutput()
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			if strings.Contains(l, `"level":"WARN"`) || strings.Contains(l, `"level":"ERROR"`) {
				out[strings.TrimPrefix(strings.TrimSuffix(c, "-1"), "gym-")]++
			}
		}
	}
	return out
}

// ---- Resumen e informe ----

func resumir(reg *registro, r resultado) resultado {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	fin := time.Now()
	r.PorOperacion, r.OrigenBusqueda = map[string]map[string]int{}, map[string]int{}
	for _, m := range reg.muestras {
		if m.T.Before(r.Detenida) || m.T.After(r.Iniciada) {
			continue
		}
		if r.PorOperacion[m.Op] == nil {
			r.PorOperacion[m.Op] = map[string]int{}
		}
		clave := fmt.Sprint(m.Status)
		if m.Code != "" {
			clave += " " + m.Code
		}
		r.PorOperacion[m.Op][clave]++
		if m.Op == "busqueda" && m.Status == 200 {
			r.OrigenBusqueda[m.Origen]++
			if m.Marcas != nil && !*m.Marcas {
				r.SinMarcas++
			}
		}
		if (m.Status == 0 || m.Status >= 500) && r.PrimeraFalla == nil {
			t := m.T
			r.PrimeraFalla = &t
		}
	}
	for _, e := range reg.estados {
		if !e.T.Before(r.Detenida) && e.Global != "ready" && r.DeteccionEstado == nil {
			t := e.T
			r.DeteccionEstado = &t
		}
	}
	for _, a := range reg.alertas {
		if !a.T.Before(r.Detenida) && !a.T.After(fin) {
			r.Alertas = append(r.Alertas, a)
		}
	}
	for _, o := range reg.outbox {
		t := o["t"].(time.Time)
		if t.Before(r.Detenida) || t.After(fin) {
			continue
		}
		n := o["pendientes"].(int)
		if n > r.OutboxMax {
			r.OutboxMax = n
		}
		r.OutboxFinal = n
	}
	r.LogsError = logsConProblemas(r.Detenida, fin)
	return r
}

func imprimir(r resultado) {
	fmt.Printf("  por operación: %v\n  origen de búsquedas: %v (sin marcas: %d)\n", r.PorOperacion, r.OrigenBusqueda, r.SinMarcas)
	if r.DeteccionEstado != nil {
		fmt.Printf("  estado agregado degradado a los %.1f s\n", r.DeteccionEstado.Sub(r.Detenida).Seconds())
	}
	for _, a := range r.Alertas {
		fmt.Printf("  alerta %s %s (%s) a los %.0f s\n", a.Nombre, a.Estado, a.Labels, a.T.Sub(r.Detenida).Seconds())
	}
	fmt.Printf("  recuperación: %.1f s después de iniciar el contenedor; outbox pendiente máx. %d, final %d\n", r.RecuperacionS, r.OutboxMax, r.OutboxFinal)
}

func informe(rs []resultado) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jornada de fallas controladas (entrega final)\n\nGenerado por `tests/resilience/jornada.go` el %s. Datos completos en los JSON de esta carpeta.\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("Tráfico de fondo: un alumno busca cada 0,5 s, reserva y cancela cada 4 s e inicia sesión cada 15 s, a través del gateway. Cada falla detiene un contenedor con `docker stop` y lo vuelve a iniciar.\n\n")
	b.WriteString("| Falla | Esperado | Búsqueda | Reserva | Login | Estado degradado | Recuperación |\n|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		det := "—"
		if r.DeteccionEstado != nil {
			det = fmt.Sprintf("%.0f s", r.DeteccionEstado.Sub(r.Detenida).Seconds())
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %.0f s |\n", r.Falla.Nombre, r.Falla.Esperado, conteo(r.PorOperacion["busqueda"]), conteo(r.PorOperacion["reserva"]), conteo(r.PorOperacion["login"]), det, r.RecuperacionS)
	}
	for _, r := range rs {
		fmt.Fprintf(&b, "\n## %s (`%s`)\n\n- Detenido: %s; iniciado: %s (hora UTC).\n- Respuestas durante la caída: %v.\n- Origen de las búsquedas exitosas: %v; sin marca «ya reservada»: %d.\n",
			r.Falla.Nombre, r.Falla.Contenedor, r.Detenida.UTC().Format("15:04:05"), r.Iniciada.UTC().Format("15:04:05"), r.PorOperacion, r.OrigenBusqueda, r.SinMarcas)
		for _, a := range r.Alertas {
			fmt.Fprintf(&b, "- Alerta `%s` %s (%s) a los %.0f s.\n", a.Nombre, a.Estado, a.Labels, a.T.Sub(r.Detenida).Seconds())
		}
		fmt.Fprintf(&b, "- Outbox de Booking pendiente: máximo %d, al final %d.\n- Líneas WARN/ERROR en logs: %v.\n- Diseño: %s.\n", r.OutboxMax, r.OutboxFinal, r.LogsError, r.Falla.Diseño)
	}
	must(os.WriteFile(filepath.Join(*salida, "RESULTADOS.md"), []byte(b.String()), 0o644), "informe")
}

func conteo(m map[string]int) string {
	if len(m) == 0 {
		return "—"
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var p []string
	for _, k := range ks {
		p = append(p, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(p, ", ")
}

// ---- HTTP y utilidades ----

var ultimoLogin struct {
	status int
	code   string
}

func login(email string) string {
	h := sha256.Sum256([]byte("carga|" + env["GYM_INTERNAL_SERVICE_KEY"] + "|" + email))
	status, body := pedir("", http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": "Carga-" + hex.EncodeToString(h[:])[:20]}, "")
	var out struct {
		Token string `json:"access_token"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &out)
	ultimoLogin.status, ultimoLogin.code = status, out.Code
	if status != 200 {
		return ""
	}
	return out.Token
}

func pedir(token, method, path string, body any, clave string) (int, []byte) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, gateway+path, reader)
	if err != nil {
		return 0, nil
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if clave != "" {
		req.Header.Set("Idempotency-Key", clave)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func leerEnv(ruta string) map[string]string {
	f, err := os.Open(ruta)
	must(err, "no se encontró .env; correr antes make demo")
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func escribirJSON(nombre string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	must(err, "serializar "+nombre)
	must(os.WriteFile(filepath.Join(*salida, nombre), raw, 0o644), "escribir "+nombre)
}

func paso(s string) { fmt.Printf("\n== %s (%s)\n", s, time.Now().Format("15:04:05")) }

func reiniciarDetenidos() {
	for c := range detenidos {
		_ = exec.Command("docker", "start", c).Run()
		delete(detenidos, c)
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "✗ "+msg)
	reiniciarDetenidos()
	os.Exit(1)
}

func must(err error, msg string) {
	if err != nil {
		fatal(msg + ": " + err.Error())
	}
}
