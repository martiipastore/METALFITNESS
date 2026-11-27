// run-flujo mide la capacidad del flujo principal del gimnasio (D13, D-97):
// búsqueda de clases, pico de reservas sobre una clase y mezcla 90/10, a través
// del gateway y con las dos instancias de Booking. Escalera de 10, 25, 50 y 100
// usuarios simultáneos (60 s de medición, 30 s de calentamiento, sin pausa);
// corta un escenario cuando el p95 supera 500 ms o los errores el 1 %.
//
//	go run tests/load/run-flujo.go              # corrida completa (~25 min)
//	go run tests/load/run-flujo.go -corta       # comprobación del entorno (~3 min)
//
// Requiere el stack de `make demo` y los datos de scripts/cargar-datos-demo.go.
// Crea sus propios datos sintéticos: 100 alumnos carga.NNN@gym.local con
// membresía y la actividad «Carga Pico». Las contraseñas se derivan de
// GYM_INTERNAL_SERVICE_KEY: no se guardan ni se imprimen.
//
// Durante la medición sube los umbrales del limitador del gateway por variables
// de entorno (sigue activo, pero no rechaza) y al final los restaura. Evidencia
// en docs/evidence/entrega-final/carga/.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gateway     = "http://localhost:8080"
	mailpit     = "http://localhost:8025"
	imagenK6    = "grafana/k6:1.3.0"
	redDocker   = "gym_default"
	alumnos     = 100
	p95Limite   = 500.0 // ms
	errorLimite = 0.01
	zona        = "America/Argentina/Buenos_Aires"
)

var (
	corta       = flag.Bool("corta", false, "comprobación breve del entorno: un nivel de 10 usuarios, 5 s de calentamiento y 15 s de medición")
	salida      = flag.String("salida", "docs/evidence/entrega-final/carga", "carpeta de evidencia")
	soloInforme = flag.Bool("informe", false, "solo regenera RESULTADOS.md desde resultados.json y environment.json de -salida, sin correr carga")
	client      = &http.Client{Timeout: 30 * time.Second}
	env         map[string]string
	loc         *time.Location
)

func main() {
	flag.Parse()
	if *soloInforme {
		var rs []resultado
		var amb map[string]any
		leerJSON(filepath.Join(*salida, "resultados.json"), &rs)
		leerJSON(filepath.Join(*salida, "environment.json"), &amb)
		informe(rs, amb)
		fmt.Println("Informe regenerado en", *salida)
		return
	}
	var err error
	if loc, err = time.LoadLocation(zona); err != nil {
		loc = time.FixedZone("ART", -3*3600)
	}
	env = leerEnv(".env")
	must(os.MkdirAll(*salida, 0o755), "carpeta de evidencia")
	datos, err := os.MkdirTemp("", "gym-carga-")
	must(err, "carpeta temporal")
	defer os.RemoveAll(datos) // credenciales sintéticas: nunca quedan en disco

	paso("Limitador del gateway: umbrales altos durante la medición")
	limitador(true)
	var una sync.Once
	restaurar = func() { una.Do(func() { paso("Restaurando el limitador"); limitador(false) }) }
	defer func() {
		if restaurar != nil {
			restaurar()
		}
	}()

	admin := login(env["MEMBERS_ADMIN_EMAIL"], env["MEMBERS_ADMIN_PASSWORD"])
	paso("Datos sintéticos")
	usuarios := asegurarAlumnos(admin)
	// Las reservas de corridas anteriores llenan las clases: se liberan antes
	// de medir y otra vez al terminar, para no dejar la demo con clases llenas.
	ses := &sesiones{usuarios: usuarios, archivo: filepath.Join(datos, "tokens.json")}
	fmt.Printf("  %d reservas futuras de corridas anteriores canceladas\n", liberarReservas(ses.vigentes()))
	defer func() {
		paso("Liberando las reservas de la carga")
		fmt.Printf("  %d reservas canceladas\n", liberarReservas(ses.vigentes()))
	}()
	profesor := asegurarPersona(admin, map[string]any{"rol": "PROFESOR", "nombre": "Carga", "apellido": "Profesor", "dni": "39000000", "email": "carga.profesor@gym.local"})
	actividad := asegurarActividad(admin, "Carga Pico", 30)
	// La actividad de prueba no debe quedar en la base ni a la vista en la web:
	// al terminar se borra con todo lo que el test creó.
	defer func() {
		paso("Retirando la actividad de prueba")
		retirarActividad(login(env["MEMBERS_ADMIN_EMAIL"], env["MEMBERS_ADMIN_PASSWORD"]), actividad)
	}()
	hoy := time.Now().In(loc)
	desde, hasta := hoy.Format(time.DateOnly), hoy.AddDate(0, 0, 13).Format(time.DateOnly)
	pool := poolDeClases(admin, desde, hasta, actividad)
	fmt.Printf("  %d alumnos, %d clases para reservar en la mezcla\n", len(usuarios), len(pool))

	ambiente := map[string]any{"fecha": time.Now().UTC().Format(time.RFC3339), "k6": imagenK6, "alumnos": len(usuarios), "clases_pool": len(pool),
		"criterio_corte": map[string]any{"p95_ms": p95Limite, "errores": errorLimite}, "corta": *corta, "docker": salidaCmd("docker", "version", "--format", "{{.Server.Version}}"),
		"cpus_host": salidaCmd("docker", "info", "--format", "{{.NCPU}}"), "memoria_host": salidaCmd("docker", "info", "--format", "{{.MemTotal}}")}
	escribirJSON(filepath.Join(*salida, "environment.json"), ambiente)

	niveles := []int{10, 25, 50, 100}
	calentamiento, medicion := "30s", "60s"
	if *corta {
		niveles, calentamiento, medicion = []int{10}, "5s", "15s"
	}
	var resultados []resultado

	for _, escenario := range []string{"busqueda", "mezcla"} {
		for _, vus := range niveles {
			paso(fmt.Sprintf("%s con %d usuarios", escenario, vus))
			escribirJSON(filepath.Join(datos, "clases.json"), map[string]any{"pool": pool, "desde": desde, "hasta": hasta})
			ses.vigentes()
			r := correr(datos, escenario, vus, calentamiento, medicion, nil)
			resultados = append(resultados, r)
			escribirJSON(filepath.Join(*salida, "resultados.json"), resultados)
			r.imprimir()
			if r.P95 > p95Limite || r.TasaErrores > errorLimite {
				fmt.Println("  criterio de corte alcanzado: no se sube más este escenario")
				break
			}
		}
	}

	// La escalera dura más que un JWT (15 min): se renueva la sesión del
	// administrador antes de crear las clases del pico.
	admin = login(env["MEMBERS_ADMIN_EMAIL"], env["MEMBERS_ADMIN_PASSWORD"])
	picos := 3
	if *corta {
		picos = 1
	}
	for n := 0; n < picos; n++ {
		paso(fmt.Sprintf("pico de reservas %d/%d: %d alumnos por una clase de 30 cupos", n+1, picos, alumnos))
		claseID := clasePico(admin, actividad, profesor.ID, hoy.AddDate(0, 0, 3), n)
		escribirJSON(filepath.Join(datos, "clases.json"), map[string]any{"pico": claseID, "pool": pool, "desde": desde, "hasta": hasta})
		ses.vigentes()
		r := correr(datos, "pico", alumnos, "", "", nil)
		r.Integridad = integridad(claseID)
		resultados = append(resultados, r)
		escribirJSON(filepath.Join(*salida, "resultados.json"), resultados)
		r.imprimir()
	}

	if !*corta {
		paso("mezcla con 25 usuarios y caída de booking-service-2 a los 20 s de medición (vuelve a los 55 s)")
		escribirJSON(filepath.Join(datos, "clases.json"), map[string]any{"pool": pool, "desde": desde, "hasta": hasta})
		eventos := []evento{{20 * time.Second, "docker", []string{"kill", "gym-booking-service-2-1"}}, {55 * time.Second, "docker", append(compose(), "start", "booking-service-2")}}
		ses.vigentes()
		r := correr(datos, "mezcla", 25, "30s", "90s", eventos)
		r.Escenario = "caida"
		resultados = append(resultados, r)
		escribirJSON(filepath.Join(*salida, "resultados.json"), resultados)
		r.imprimir()
		esperarListo()
	}

	escribirJSON(filepath.Join(*salida, "resultados.json"), resultados)
	informe(resultados, ambiente)
	fmt.Println("\nEvidencia en", *salida)
}

// ---- Ejecución de k6 y muestreo ----

type evento struct {
	despues time.Duration
	cmd     string
	args    []string
}

type recurso struct {
	CPUProm, CPUMax float64
	MemMaxMiB       float64
}

type resultado struct {
	Escenario     string
	Usuarios      int
	Operaciones   float64
	OpsPorSegundo float64
	P50, P95, P99 float64 // ms; búsqueda o reserva según el escenario
	ReservaP95    float64
	Errores       float64
	TasaErrores   float64
	ErroresPor    map[string]float64
	Confirmadas   float64
	Negocio409    float64
	Reparto       map[string]float64
	Origen        map[string]float64
	Recursos      map[string]recurso
	Integridad    map[string]any `json:",omitempty"`
	Sucesos       []string       `json:",omitempty"`
}

func (r resultado) imprimir() {
	fmt.Printf("  ops=%.0f (%.1f/s) p50=%.0f p95=%.0f p99=%.0f ms errores=%.2f%% reparto=%v\n", r.Operaciones, r.OpsPorSegundo, r.P50, r.P95, r.P99, r.TasaErrores*100, r.Reparto)
	if r.Escenario == "pico" {
		fmt.Printf("  confirmadas=%.0f sin_cupo/otras_409=%.0f integridad=%v\n", r.Confirmadas, r.Negocio409, r.Integridad)
	}
}

func correr(datos, escenario string, vus int, calentamiento, medicion string, eventos []evento) resultado {
	abs, _ := filepath.Abs("tests/load")
	_ = os.Remove(filepath.Join(datos, "resumen.json"))
	antesGW := metricas(gateway + "/metrics")
	antesB := sumar(metricas("http://localhost:8082/metrics"), metricas("http://localhost:8087/metrics"))

	ctx, cancel := context.WithCancel(context.Background())
	muestras := make(chan map[string][2]float64, 1000)
	var calent time.Duration
	if d, err := time.ParseDuration(calentamiento); err == nil {
		calent = d
	}
	// La medición empieza cuando Booking recibe la primera solicitud del test
	// más el calentamiento: las
	// muestras de CPU y los sucesos usan esa referencia, no una estimación.
	inicio := make(chan time.Time, 1)
	go func() {
		base := totalBooking(metricas(gateway + "/metrics"))
		for ctx.Err() == nil {
			if totalBooking(metricas(gateway+"/metrics")) > base {
				inicio <- time.Now().Add(calent)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	var sucesos []string
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		var t0 time.Time
		select {
		case t0 = <-inicio:
		case <-ctx.Done():
			return
		}
		for _, e := range eventos {
			e := e
			go func() {
				time.Sleep(time.Until(t0.Add(e.despues)))
				salidaCmd(e.cmd, e.args...)
				mu.Lock()
				sucesos = append(sucesos, fmt.Sprintf("%s (+%.1f s) %s %s", time.Now().UTC().Format("15:04:05.000"), time.Since(t0).Seconds(), e.cmd, strings.Join(e.args[len(e.args)-2:], " ")))
				mu.Unlock()
			}()
		}
		time.Sleep(time.Until(t0))
		muestrear(ctx, muestras)
	}()

	args := []string{"run", "--rm", "--network", redDocker, "-v", datos + ":/datos", "-v", abs + ":/scripts:ro",
		"-e", "ESCENARIO=" + escenario, "-e", "VUS=" + strconv.Itoa(vus), "-e", "CALENTAMIENTO=" + calentamiento, "-e", "MEDICION=" + medicion,
		imagenK6, "run", "--quiet", "/scripts/flujo.js"}
	out, err := exec.Command("docker", args...).CombinedOutput()
	cancel()
	wg.Wait()
	close(muestras)
	if err != nil && !strings.Contains(err.Error(), "exit status 99") {
		fmt.Println(string(out))
		must(err, "k6 no completó el escenario")
	}

	var resumen struct {
		Metrics map[string]struct {
			Values map[string]float64 `json:"values"`
		} `json:"metrics"`
	}
	raw, err := os.ReadFile(filepath.Join(datos, "resumen.json"))
	must(err, "k6 no generó resumen")
	must(json.Unmarshal(raw, &resumen), "resumen de k6")
	v := func(m, k string) float64 { return resumen.Metrics[m].Values[k] }

	r := resultado{Escenario: escenario, Usuarios: vus, Operaciones: v("medicion_operaciones", "count"), Errores: v("medicion_errores", "count"),
		Confirmadas: v("reservas_confirmadas", "count"), Negocio409: v("reservas_negocio_409", "count"), ErroresPor: map[string]float64{}}
	trend := "medicion_busqueda_ms"
	if escenario == "pico" {
		trend = "medicion_reserva_ms"
	}
	r.P50, r.P95, r.P99 = v(trend, "med"), v(trend, "p(95)"), v(trend, "p(99)")
	r.ReservaP95 = v("medicion_reserva_ms", "p(95)")
	for nombre, m := range resumen.Metrics {
		if strings.HasPrefix(nombre, "medicion_errores{") && m.Values["count"] > 0 {
			r.ErroresPor[nombre] = m.Values["count"]
		}
	}
	if d, err := time.ParseDuration(medicion); err == nil && d > 0 {
		r.OpsPorSegundo = r.Operaciones / d.Seconds()
	}
	if r.Operaciones > 0 {
		r.TasaErrores = r.Errores / r.Operaciones
	}
	despuesGW := metricas(gateway + "/metrics")
	r.Reparto = map[string]float64{}
	for k, val := range despuesGW {
		if strings.HasPrefix(k, "gateway_balancer_requests_total") {
			r.Reparto[instancia(k)] = val - antesGW[k]
		}
	}
	despuesB := sumar(metricas("http://localhost:8082/metrics"), metricas("http://localhost:8087/metrics"))
	r.Origen = map[string]float64{}
	for k, val := range despuesB {
		if strings.HasPrefix(k, "classes_search_responses_total") {
			r.Origen[strings.TrimSuffix(strings.TrimPrefix(k, `classes_search_responses_total{origen="`), `"}`)] = val - antesB[k]
		}
	}
	r.Recursos = resumirRecursos(muestras)
	r.Sucesos = sucesos
	return r
}

// muestrear toma CPU y memoria de los contenedores del gimnasio cada 5 s.
func muestrear(ctx context.Context, out chan<- map[string][2]float64) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		texto := salidaCmd("docker", "stats", "--no-stream", "--format", "{{.Name}};{{.CPUPerc}};{{.MemUsage}}")
		m := map[string][2]float64{}
		for _, l := range strings.Split(texto, "\n") {
			p := strings.Split(l, ";")
			if len(p) != 3 || !strings.HasPrefix(p[0], "gym-") {
				continue
			}
			cpu, _ := strconv.ParseFloat(strings.TrimSuffix(p[1], "%"), 64)
			m[strings.TrimSuffix(strings.TrimPrefix(p[0], "gym-"), "-1")] = [2]float64{cpu, mib(strings.Split(p[2], " / ")[0])}
		}
		select {
		case out <- m:
		default:
		}
	}
}

func resumirRecursos(muestras <-chan map[string][2]float64) map[string]recurso {
	suma, n := map[string]recurso{}, map[string]int{}
	for m := range muestras {
		for nombre, v := range m {
			r := suma[nombre]
			r.CPUProm += v[0]
			if v[0] > r.CPUMax {
				r.CPUMax = v[0]
			}
			if v[1] > r.MemMaxMiB {
				r.MemMaxMiB = v[1]
			}
			suma[nombre] = r
			n[nombre]++
		}
	}
	for nombre, r := range suma {
		r.CPUProm /= float64(n[nombre])
		suma[nombre] = r
	}
	return suma
}

func mib(s string) float64 {
	s = strings.TrimSpace(s)
	for suf, f := range map[string]float64{"GiB": 1024, "MiB": 1, "KiB": 1.0 / 1024, "kB": 1.0 / 1024, "B": 1.0 / 1048576} {
		if strings.HasSuffix(s, suf) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, suf), 64)
			return v * f
		}
	}
	return 0
}

var reMetrica = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})?) ([0-9.eE+-]+)$`)

func metricas(u string) map[string]float64 {
	out := map[string]float64{}
	res, err := client.Get(u)
	if err != nil {
		return out
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if m := reMetrica.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			v, _ := strconv.ParseFloat(m[3], 64)
			out[m[1]] = v
		}
	}
	return out
}

func sumar(a, b map[string]float64) map[string]float64 {
	for k, v := range b {
		a[k] += v
	}
	return a
}

func totalBooking(m map[string]float64) float64 {
	t := 0.0
	for k, v := range m {
		if strings.HasPrefix(k, "gateway_balancer_requests_total") {
			t += v
		}
	}
	return t
}

func instancia(serie string) string {
	i := strings.Index(serie, `instancia="`)
	if i < 0 {
		return serie
	}
	resto := serie[i+len(`instancia="`):]
	return resto[:strings.Index(resto, `"`)]
}

// integridad comprueba en booking_db, después del pico, que la clase no supere
// su cupo, que la ocupación coincida con las reservas y que no haya duplicados
// en todo el sistema (RN-13, RN-14).
func integridad(claseID string) map[string]any {
	q := func(sql string) string {
		return strings.TrimSpace(salidaCmd("docker", "exec", "gym-postgres-1", "psql", "-U", envOr("POSTGRES_SUPERUSER", "postgres"), "-d", "booking_db", "-tAc", sql))
	}
	return map[string]any{
		"capacidad":                   q(fmt.Sprintf("SELECT capacidad FROM clases WHERE id='%s'", claseID)),
		"ocupacion":                   q(fmt.Sprintf("SELECT ocupacion FROM clases WHERE id='%s'", claseID)),
		"reservas_confirmadas":        q(fmt.Sprintf("SELECT count(*) FROM reservas WHERE clase_id='%s' AND estado='CONFIRMADA'", claseID)),
		"clases_sobre_cupo":           q("SELECT count(*) FROM clases WHERE ocupacion > capacidad"),
		"reservas_activas_duplicadas": q("SELECT count(*) FROM (SELECT clase_id, alumno_id FROM reservas WHERE estado <> 'CANCELADA' GROUP BY 1,2 HAVING count(*) > 1) d"),
	}
}

// ---- Limitador y stack ----

func compose() []string {
	return []string{"compose", "--env-file", ".env", "-f", "deploy/docker-compose.yml", "--profile", "search", "--profile", "identity"}
}

func limitador(alto bool) {
	cmd := exec.Command("docker", append(compose(), "up", "-d", "--no-deps", "--wait", "api-gateway")...)
	cmd.Env = os.Environ()
	if alto {
		cmd.Env = append(cmd.Env, "GATEWAY_RATE_LIMIT_LOGIN_PER_MINUTE=100000", "GATEWAY_RATE_LIMIT_USER_RATE=100000", "GATEWAY_RATE_LIMIT_USER_BURST=100000")
	} else {
		for _, k := range []string{"GATEWAY_RATE_LIMIT_LOGIN_PER_MINUTE", "GATEWAY_RATE_LIMIT_USER_RATE", "GATEWAY_RATE_LIMIT_USER_BURST"} {
			cmd.Env = append(cmd.Env, k+"="+envOr(k, map[string]string{"GATEWAY_RATE_LIMIT_LOGIN_PER_MINUTE": "5", "GATEWAY_RATE_LIMIT_USER_RATE": "10", "GATEWAY_RATE_LIMIT_USER_BURST": "20"}[k]))
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(out))
		must(err, "recrear el gateway")
	}
	esperarListo()
}

func esperarListo() {
	for i := 0; i < 60; i++ {
		res, err := client.Get(gateway + "/api/v1/status")
		if err == nil {
			var s struct{ Status string }
			_ = json.NewDecoder(res.Body).Decode(&s)
			res.Body.Close()
			if s.Status == "ready" {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("  aviso: el sistema no volvió a ready en 2 minutos")
}

// ---- Datos sintéticos por el gateway ----

type credencial struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func password(email string) string {
	h := sha256.Sum256([]byte("carga|" + env["GYM_INTERNAL_SERVICE_KEY"] + "|" + email))
	return "Carga-" + hex.EncodeToString(h[:])[:20]
}

func asegurarAlumnos(admin string) []credencial {
	out := make([]credencial, 0, alumnos)
	desde := time.Now().Add(-time.Minute)
	hoy := time.Now().In(loc).Format(time.DateOnly)
	for n := 1; n <= alumnos; n++ {
		email := fmt.Sprintf("carga.%03d@gym.local", n)
		p := asegurarPersona(admin, map[string]any{"rol": "ALUMNO", "nombre": "Carga", "apellido": fmt.Sprintf("Alumno %03d", n), "dni": strconv.Itoa(39100000 + n), "email": email, "fecha_nacimiento": "2000-01-01"})
		if p.ActivacionPendiente {
			activar(email, password(email), desde)
		}
		var actual struct {
			Vigente any `json:"membresia_vigente"`
		}
		pedir(admin, http.MethodGet, "/api/v1/memberships/users/"+p.ID, nil, "", &actual)
		if actual.Vigente == nil {
			pedir(admin, http.MethodPost, "/api/v1/memberships", map[string]any{"usuario_id": p.ID, "tipo_codigo": "MENSUAL", "fecha_inicio": hoy, "motivo": "Datos de carga"}, "carga-membresia-"+p.ID+"-"+hoy, nil)
		}
		out = append(out, credencial{email, password(email)})
	}
	return out
}

type persona struct {
	ID                  string `json:"id"`
	ActivacionPendiente bool   `json:"activacion_pendiente"`
}

func asegurarPersona(admin string, alta map[string]any) persona {
	var p persona
	if st := pedirEstado(admin, http.MethodPost, "/api/v1/users", alta, "carga-alta-"+alta["dni"].(string), &p); st == 201 || st == 200 {
		return p
	}
	var pagina struct {
		Items []persona `json:"items"`
	}
	pedir(admin, http.MethodGet, "/api/v1/users?q="+url.QueryEscape(alta["dni"].(string)), nil, "", &pagina)
	if len(pagina.Items) == 0 {
		must(fmt.Errorf("sin resultados"), "crear o encontrar "+alta["email"].(string))
	}
	return pagina.Items[0]
}

func activar(email, pass string, desde time.Time) {
	for i := 0; i < 30; i++ {
		if token := tokenActivacion(email, desde); token != "" {
			pedir("", http.MethodPost, "/api/v1/auth/activate", map[string]any{"token": token, "password": pass}, "", nil)
			return
		}
		time.Sleep(time.Second)
	}
	must(fmt.Errorf("sin correo"), "activación de "+email)
}

func tokenActivacion(email string, desde time.Time) string {
	var lista struct {
		Messages []struct {
			ID      string
			Created time.Time
		}
	}
	res, err := client.Get(mailpit + "/api/v1/search?query=" + url.QueryEscape(`to:"`+email+`"`))
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if json.NewDecoder(res.Body).Decode(&lista) != nil {
		return ""
	}
	for _, m := range lista.Messages {
		if m.Created.Before(desde) {
			continue
		}
		res, err := client.Get(mailpit + "/api/v1/message/" + m.ID)
		if err != nil {
			return ""
		}
		var d struct{ Text string }
		_ = json.NewDecoder(res.Body).Decode(&d)
		res.Body.Close()
		for _, l := range strings.Split(d.Text, "\n") {
			l = strings.TrimSpace(l)
			if u, err := url.Parse(l); err == nil && strings.HasPrefix(l, "http") {
				if v, err := url.ParseQuery(u.Fragment); err == nil && v.Get("activate") != "" {
					return v.Get("activate")
				}
			}
		}
	}
	return ""
}

func asegurarActividad(admin, nombre string, capacidad int) string {
	var lista struct {
		Items []struct {
			ID, Nombre string
			Activa     bool `json:"activa"`
		} `json:"items"`
	}
	pedir(admin, http.MethodGet, "/api/v1/activities", nil, "", &lista)
	for _, a := range lista.Items {
		if a.Nombre == nombre {
			if !a.Activa {
				pedir(admin, http.MethodPut, "/api/v1/activities/"+a.ID, map[string]any{"nombre": nombre, "capacidad_maxima": capacidad, "activa": true}, "", nil)
			}
			return a.ID
		}
	}
	var a struct{ ID string }
	pedir(admin, http.MethodPost, "/api/v1/activities", map[string]any{"nombre": nombre, "capacidad_maxima": capacidad}, "", &a)
	return a.ID
}

// retirarActividad borra la actividad de prueba con todo lo que el test creó
// (horarios, clases y reservas): es un dato sintético y no debe quedar en la
// base ni a la vista en la web. Después reconstruye el índice de búsqueda y
// limpia la caché de Booking. El test nunca registra asistencias en ella.
func retirarActividad(_ string, actividad string) {
	sql := fmt.Sprintf(`BEGIN;
DELETE FROM reservas WHERE clase_id IN (SELECT id FROM clases WHERE actividad_id = '%[1]s');
DELETE FROM clases WHERE actividad_id = '%[1]s';
DELETE FROM horarios WHERE actividad_id = '%[1]s';
DELETE FROM actividades WHERE id = '%[1]s';
COMMIT;`, actividad)
	if out, err := exec.Command("docker", "exec", "gym-postgres-1", "psql", "-U", "postgres", "-d", "booking_db", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput(); err != nil {
		fmt.Println("  aviso: no se pudo borrar la actividad de prueba:", strings.TrimSpace(string(out)))
		return
	}
	if out, err := exec.Command("docker", append(compose(), "run", "--rm", "booking-indexer", "-reindex")...).CombinedOutput(); err != nil {
		fmt.Println("  aviso: no se pudo reconstruir el índice:", strings.TrimSpace(string(out)))
	}
	pw := envOr("REDIS_PASSWORD", "")
	_ = exec.Command("docker", "exec", "gym-redis-1", "sh", "-c", "redis-cli -a \"$1\" --no-auth-warning --scan --pattern 'booking:*' | xargs -r redis-cli -a \"$1\" --no-auth-warning del", "sh", pw).Run()
	fmt.Println("  actividad de prueba borrada, índice reconstruido y caché limpia")
}

type clase struct {
	ID        string              `json:"id"`
	Actividad struct{ ID string } `json:"actividad"`
	Inicio    string              `json:"inicio"`
	Ocupacion int                 `json:"ocupacion"`
}

func poolDeClases(admin, desde, hasta, excluir string) []string {
	var ids []string
	for pagina := 1; pagina <= 10; pagina++ {
		var p struct {
			Items []clase `json:"items"`
			Total int     `json:"total"`
		}
		pedir(admin, http.MethodGet, fmt.Sprintf("/api/v1/classes?fecha_desde=%s&fecha_hasta=%s&solo_disponibles=true&pagina=%d&tamano=100", desde, hasta, pagina), nil, "", &p)
		for _, c := range p.Items {
			if c.Actividad.ID != excluir {
				ids = append(ids, c.ID)
			}
		}
		if pagina*100 >= p.Total {
			break
		}
	}
	if len(ids) == 0 {
		must(fmt.Errorf("sin clases futuras"), "correr antes scripts/cargar-datos-demo.go")
	}
	return ids
}

// sesiones inicia sesión una vez por alumno sintético y reutiliza los tokens en
// todas las corridas de k6. Members limita el login a 120 por minuto en todo el
// sistema (protección contra fuerza bruta, CB-03) y esa protección no se toca:
// los tokens se renuevan cuando tienen más de 8 minutos (duran 15) y nunca dos
// veces dentro del mismo minuto.
type sesiones struct {
	usuarios  []credencial
	archivo   string
	tokens    []string
	obtenidas time.Time
}

func (s *sesiones) vigentes() []string {
	if s.tokens != nil && time.Since(s.obtenidas) < 8*time.Minute {
		return s.tokens
	}
	if espera := 61*time.Second - time.Since(s.obtenidas); s.tokens != nil && espera > 0 {
		time.Sleep(espera)
	}
	tokens := make([]string, len(s.usuarios))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for i, u := range s.usuarios {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u credencial) {
			defer func() { <-sem; wg.Done() }()
			tokens[i] = login(u.Email, u.Password)
		}(i, u)
	}
	wg.Wait()
	s.tokens, s.obtenidas = tokens, time.Now()
	escribirJSON(s.archivo, tokens)
	return tokens
}

// liberarReservas cancela, por la API y como cada alumno sintético, sus
// reservas confirmadas que todavía admiten cancelación (más de 1 hora antes,
// D-02). Devuelve cuántas canceló.
func liberarReservas(tokens []string) int {
	var total int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	limite := time.Now().Add(65 * time.Minute)
	for _, token := range tokens {
		wg.Add(1)
		sem <- struct{}{}
		go func(token string) {
			defer func() { <-sem; wg.Done() }()
			var ids []string
			for pagina := 1; pagina <= 50; pagina++ {
				var p struct {
					Items []struct {
						ID    string `json:"id"`
						Clase struct {
							Inicio time.Time `json:"inicio"`
						} `json:"clase"`
					} `json:"items"`
				}
				pedir(token, http.MethodGet, fmt.Sprintf("/api/v1/bookings/me?estado=CONFIRMADA&pagina=%d&tamano=50", pagina), nil, "", &p)
				for _, r := range p.Items {
					if r.Clase.Inicio.After(limite) {
						ids = append(ids, r.ID)
					}
				}
				if len(p.Items) < 50 {
					break
				}
			}
			for _, id := range ids {
				pedir(token, http.MethodPost, "/api/v1/bookings/"+id+"/cancel", map[string]any{}, "carga-liberar-"+id, nil)
			}
			mu.Lock()
			total += len(ids)
			mu.Unlock()
		}(token)
	}
	wg.Wait()
	return total
}

// clasePico crea una clase nueva de 30 cupos en dia, en el primer horario libre
// desde las 05:00 (n desplaza la búsqueda para que cada pico use otra hora y
// los alumnos no tengan reservas superpuestas).
func clasePico(admin, actividad, profesor string, dia time.Time, n int) string {
	fecha := dia.Format(time.DateOnly)
	dow := int(dia.Weekday())
	if dow == 0 {
		dow = 7
	}
	var horarios struct {
		Items []struct {
			DiaSemana int    `json:"dia_semana"`
			Inicio    string `json:"inicio"`
		} `json:"items"`
	}
	pedir(admin, http.MethodGet, "/api/v1/activities/"+actividad+"/schedules", nil, "", &horarios)
	ocupado := map[string]bool{}
	for _, h := range horarios.Items {
		if h.DiaSemana == dow {
			ocupado[h.Inicio] = true
		}
	}
	for h := 5 + n; h < 23; h++ {
		inicio := fmt.Sprintf("%02d:00", h)
		if ocupado[inicio] {
			continue
		}
		pedir(admin, http.MethodPost, "/api/v1/activities/"+actividad+"/schedules", map[string]any{"dia_semana": dow, "inicio": inicio, "duracion_minutos": 30, "profesor_id": profesor, "capacidad": 30}, "", nil)
		pedir(admin, http.MethodPost, "/api/v1/classes/generate", map[string]any{"desde": fecha, "hasta": fecha}, "", nil)
		for i := 0; i < 20; i++ {
			var p struct {
				Items []clase `json:"items"`
			}
			pedir(admin, http.MethodGet, fmt.Sprintf("/api/v1/classes?fecha_desde=%s&fecha_hasta=%s&actividad_id=%s&franja=%s", fecha, fecha, actividad, inicio), nil, "", &p)
			if len(p.Items) == 1 && p.Items[0].Ocupacion == 0 {
				return p.Items[0].ID
			}
			time.Sleep(500 * time.Millisecond) // el índice tarda alrededor de 1 s
		}
	}
	must(fmt.Errorf("sin horario libre"), "crear la clase del pico")
	return ""
}

// ---- HTTP ----

func login(email, pass string) string {
	var out struct {
		Token string `json:"access_token"`
	}
	pedir("", http.MethodPost, "/api/v1/auth/login", map[string]any{"email": email, "password": pass}, "", &out)
	return out.Token
}

func pedir(token, method, path string, body any, clave string, out any) {
	if st := pedirEstado(token, method, path, body, clave, out); st >= 300 {
		must(fmt.Errorf("HTTP %d", st), method+" "+path)
	}
}

var contadorClaves atomic.Int64

func pedirEstado(token, method, path string, body any, clave string, out any) int {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, gateway+path, reader)
	must(err, "solicitud")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		if clave == "" {
			clave = fmt.Sprintf("carga-%d-%d", time.Now().UnixNano(), contadorClaves.Add(1))
		}
		req.Header.Set("Idempotency-Key", clave)
	}
	res, err := client.Do(req)
	must(err, "el gateway no responde en localhost:8080")
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 && res.StatusCode != 409 {
		fmt.Fprintf(os.Stderr, "  %s %s → %d: %s\n", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && res.StatusCode < 300 && len(raw) > 0 {
		must(json.Unmarshal(raw, out), "respuesta de "+path)
	}
	return res.StatusCode
}

// ---- Informe ----

func informe(rs []resultado, amb map[string]any) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resultados de carga del flujo principal (D13)\n\nGenerado por `tests/load/run-flujo.go` el %s. Ambiente en `environment.json`; datos completos en `resultados.json`.\n\n", amb["fecha"])
	fmt.Fprintf(&b, "Escalera de usuarios simultáneos sin pausa, a través del gateway con las dos instancias de Booking. Percentiles de la ventana de medición (sin calentamiento). Corte: p95 > %.0f ms o errores > %.0f %%.\n\n", p95Limite, errorLimite*100)
	b.WriteString("| Escenario | Usuarios | Ops/s | p50 ms | p95 ms | p99 ms | Errores | Reparto booking-1 / booking-2 | Origen de búsquedas (fuente/caché/vencida) | CPU máx. (contenedor) |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		top, cpu := "", 0.0
		for n, rec := range r.Recursos {
			if rec.CPUMax > cpu {
				top, cpu = n, rec.CPUMax
			}
		}
		ops := fmt.Sprintf("%.1f", r.OpsPorSegundo)
		if r.Escenario == "pico" {
			ops = "—" // ráfaga de una reserva por usuario: no hay ventana fija
		}
		origen := fmt.Sprintf("%.0f / %.0f / %.0f", r.Origen["fuente"], r.Origen["cache"], r.Origen["cache_vencida"])
		if r.Origen["fuente"] < 0 || r.Origen["cache"] < 0 || r.Origen["cache_vencida"] < 0 {
			origen = "n/d (la instancia reiniciada pone sus contadores en cero)"
		} else if r.Escenario == "pico" {
			origen = "— (solo reservas)"
		}
		maximo := fmt.Sprintf("%.0f %% (%s)", cpu, top)
		if top == "" {
			maximo = "— (ráfaga más corta que el muestreo)"
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %.0f | %.0f | %.0f | %.2f %% | %.0f / %.0f | %s | %s |\n", r.Escenario, r.Usuarios, ops, r.P50, r.P95, r.P99, r.TasaErrores*100,
			r.Reparto["booking-service-1"], r.Reparto["booking-service-2"], origen, maximo)
	}
	b.WriteString("\n## Picos de reservas e integridad\n\n| Pico | Confirmadas | 409 de negocio | Errores | p95 reserva ms | Capacidad | Ocupación | Reservas confirmadas | Clases sobre cupo | Duplicadas |\n|---|---|---|---|---|---|---|---|---|---|\n")
	n := 0
	for _, r := range rs {
		if r.Escenario != "pico" {
			continue
		}
		n++
		i := r.Integridad
		fmt.Fprintf(&b, "| %d | %.0f | %.0f | %.0f | %.0f | %v | %v | %v | %v | %v |\n", n, r.Confirmadas, r.Negocio409, r.Errores, r.P95, i["capacidad"], i["ocupacion"], i["reservas_confirmadas"], i["clases_sobre_cupo"], i["reservas_activas_duplicadas"])
	}
	for _, r := range rs {
		if r.Escenario == "caida" {
			fmt.Fprintf(&b, "\n## Caída de una instancia durante la carga\n\nMezcla con 25 usuarios; sucesos: %s. Errores %.0f de %.0f (%.2f %%), por tipo: %v. Reparto: %v.\n", strings.Join(r.Sucesos, "; "), r.Errores, r.Operaciones, r.TasaErrores*100, r.ErroresPor, r.Reparto)
		}
	}
	b.WriteString("\n## Recursos por escenario (CPU promedio / máxima, memoria máxima)\n\n")
	for _, r := range rs {
		nombres := make([]string, 0, len(r.Recursos))
		for k := range r.Recursos {
			nombres = append(nombres, k)
		}
		sort.Slice(nombres, func(i, j int) bool { return r.Recursos[nombres[i]].CPUProm > r.Recursos[nombres[j]].CPUProm })
		if len(nombres) > 5 {
			nombres = nombres[:5]
		}
		if len(nombres) == 0 {
			continue // los picos duran menos que un ciclo de muestreo
		}
		partes := []string{}
		for _, k := range nombres {
			x := r.Recursos[k]
			partes = append(partes, fmt.Sprintf("%s %.0f/%.0f %% %.0f MiB", k, x.CPUProm, x.CPUMax, x.MemMaxMiB))
		}
		fmt.Fprintf(&b, "- **%s, %d usuarios:** %s\n", r.Escenario, r.Usuarios, strings.Join(partes, "; "))
	}
	must(os.WriteFile(filepath.Join(*salida, "RESULTADOS.md"), []byte(b.String()), 0o644), "informe")
}

// ---- Utilidades ----

func leerEnv(ruta string) map[string]string {
	f, err := os.Open(ruta)
	must(err, "no se encontró .env; correr antes make demo")
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := env[k]; v != "" {
		return v
	}
	return def
}

func salidaCmd(cmd string, args ...string) string {
	out, _ := exec.Command(cmd, args...).Output()
	return strings.TrimSpace(string(out))
}

func leerJSON(ruta string, v any) {
	raw, err := os.ReadFile(ruta)
	must(err, "leer "+ruta)
	must(json.Unmarshal(raw, v), "interpretar "+ruta)
}

func escribirJSON(ruta string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	must(err, "json")
	must(os.WriteFile(ruta, raw, 0o600), ruta)
}

func paso(s string) { fmt.Printf("\n== %s (%s)\n", s, time.Now().Format("15:04:05")) }

// restaurar vuelve el limitador a su configuración; must lo llama antes de
// salir porque os.Exit no ejecuta los defer.
var restaurar func()

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", msg, err)
		if restaurar != nil {
			restaurar()
		}
		os.Exit(1)
	}
}
