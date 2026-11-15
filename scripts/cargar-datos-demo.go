// cargar-datos-demo carga, a través del gateway, los datos mínimos para
// demostrar la búsqueda de clases (D-95): dos profesores, una alumna activada
// con membresía mensual, horarios en cinco actividades y las clases de las
// próximas dos semanas. Se puede correr más de una vez: lo que ya existe se
// saltea. Requiere el stack de `make demo` levantado.
//
//	go run scripts/cargar-datos-demo.go
//
// Lee el administrador de .env. La contraseña de la alumna se genera al
// activarla y se imprime una sola vez; no se guarda en el repositorio.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	gateway = "http://localhost:8080/api/v1"
	mailpit = "http://localhost:8025"
	zona    = "America/Argentina/Buenos_Aires"
)

var client = &http.Client{Timeout: 15 * time.Second}

type persona struct {
	ID                  string `json:"id"`
	Nombre              string `json:"nombre"`
	Apellido            string `json:"apellido"`
	DNI                 string `json:"dni"`
	Email               string `json:"email"`
	ActivacionPendiente bool   `json:"activacion_pendiente"`
}

type horario struct {
	actividad, profesor string
	dias                []int
	inicio              string
	duracion, capacidad int
}

func main() {
	env := leerEnv(".env")
	token := login(env["MEMBERS_ADMIN_EMAIL"], env["MEMBERS_ADMIN_PASSWORD"])
	fmt.Println("✓ sesión de administrador")

	lucia := asegurarPersona(token, map[string]any{"rol": "PROFESOR", "nombre": "Lucía", "apellido": "Gómez", "dni": "30111222", "email": "lucia.gomez@gym.local"})
	juan := asegurarPersona(token, map[string]any{"rol": "PROFESOR", "nombre": "Juan", "apellido": "Ruiz", "dni": "30333444", "email": "juan.ruiz@gym.local"})
	desde := time.Now().Add(-time.Minute) // solo correos llegados desde esta carga
	alumna := asegurarPersona(token, map[string]any{"rol": "ALUMNO", "nombre": "Martina", "apellido": "Demo", "dni": "40555666", "email": "alumna@gym.local", "fecha_nacimiento": "2002-03-15"})
	fmt.Println("✓ profesores Lucía Gómez y Juan Ruiz; alumna Martina Demo")

	password := ""
	if alumna.ActivacionPendiente {
		password = activar(alumna.Email, desde)
		// Se muestra ya: si un paso posterior falla, la contraseña no se pierde.
		fmt.Printf("✓ alumna activada: alumna@gym.local  contraseña: %s  (anotala: se muestra solo ahora)\n", password)
	} else {
		fmt.Println("· la alumna ya estaba activada; su contraseña no cambia")
	}

	asegurarMembresia(token, alumna.ID)

	profesores := map[string]string{"lucia": lucia.ID, "juan": juan.ID}
	actividades := map[string]string{}
	var lista struct {
		Items []struct{ ID, Nombre string } `json:"items"`
	}
	pedir(token, http.MethodGet, "/activities", nil, "", &lista)
	for _, a := range lista.Items {
		actividades[a.Nombre] = a.ID
	}
	horarios := []horario{
		{"Zumba", "lucia", []int{1, 3}, "19:00", 60, 20},
		{"Funcional", "juan", []int{2, 4}, "18:00", 60, 25},
		{"Strong Nation", "lucia", []int{5}, "19:00", 60, 20},
		{"GAP", "juan", []int{6}, "10:00", 60, 20},
		{"Musculación", "lucia", []int{1, 2, 3, 4, 5}, "10:00", 0, 30},
	}
	creados := 0
	for _, h := range horarios {
		creados += asegurarHorarios(token, actividades[h.actividad], profesores[h.profesor], h)
	}
	fmt.Printf("✓ horarios: %d nuevos\n", creados)

	hoy := hoyArgentina()
	var generadas struct {
		Creadas int `json:"creadas"`
	}
	pedir(token, http.MethodPost, "/classes/generate", map[string]any{"desde": hoy.Format(time.DateOnly), "hasta": hoy.AddDate(0, 0, 14).Format(time.DateOnly)}, "", &generadas)
	fmt.Printf("✓ clases generadas para las próximas dos semanas: %d nuevas\n", generadas.Creadas)

	// El índice se actualiza por eventos (~1 s) y la primera consulta a un
	// OpenSearch recién levantado puede superar el timeout de 2 s (503): se
	// reintenta hasta ver las clases.
	var pagina struct {
		Total int `json:"total"`
	}
	for i := 0; i < 20; i++ {
		status := pedirEstado(token, http.MethodGet, "/classes?q=zum&fecha_desde="+hoy.Format(time.DateOnly)+"&fecha_hasta="+hoy.AddDate(0, 0, 14).Format(time.DateOnly), nil, "", &pagina)
		if status == 200 && pagina.Total > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	fmt.Printf("✓ la búsqueda «zum» encuentra %d clases de Zumba\n", pagina.Total)

	fmt.Println()
	fmt.Println("Listo. Web: http://localhost:5173")
	fmt.Printf("  Administrador: %s (contraseña MEMBERS_ADMIN_PASSWORD de .env)\n", env["MEMBERS_ADMIN_EMAIL"])
	if password != "" {
		fmt.Printf("  Alumna:        alumna@gym.local  contraseña: %s\n", password)
	} else {
		fmt.Println("  Alumna:        alumna@gym.local (contraseña mostrada en la primera carga)")
	}
}

func hoyArgentina() time.Time {
	loc, err := time.LoadLocation(zona)
	if err != nil {
		loc = time.FixedZone("ART", -3*3600)
	}
	return time.Now().In(loc)
}

func leerEnv(ruta string) map[string]string {
	f, err := os.Open(ruta)
	salir(err, "no se encontró .env; corré primero make demo")
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		linea := strings.TrimSpace(sc.Text())
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		if k, v, ok := strings.Cut(linea, "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func login(email, password string) string {
	var out struct {
		Token string `json:"access_token"`
	}
	pedir("", http.MethodPost, "/auth/login", map[string]any{"email": email, "password": password}, "", &out)
	return out.Token
}

func asegurarPersona(token string, alta map[string]any) persona {
	var p persona
	status := pedirEstado(token, http.MethodPost, "/users", alta, "demo-alta-"+alta["dni"].(string), &p)
	if status == 201 || status == 200 {
		return p
	}
	var pagina struct {
		Items []persona `json:"items"`
	}
	pedir(token, http.MethodGet, "/users?q="+url.QueryEscape(alta["dni"].(string)), nil, "", &pagina)
	if len(pagina.Items) == 0 {
		salir(fmt.Errorf("HTTP %d", status), "no se pudo crear ni encontrar a "+alta["email"].(string))
	}
	return pagina.Items[0]
}

func asegurarMembresia(token, alumnoID string) {
	var actual struct {
		Vigente any `json:"membresia_vigente"`
	}
	pedir(token, http.MethodGet, "/memberships/users/"+alumnoID, nil, "", &actual)
	if actual.Vigente != nil {
		fmt.Println("· la alumna ya tenía membresía vigente")
		return
	}
	hoy := hoyArgentina().Format(time.DateOnly)
	pedir(token, http.MethodPost, "/memberships", map[string]any{"usuario_id": alumnoID, "tipo_codigo": "MENSUAL", "fecha_inicio": hoy, "motivo": "Datos de demostración"}, "demo-membresia-"+hoy, nil)
	fmt.Println("✓ membresía mensual desde hoy")
}

func asegurarHorarios(token, actividadID, profesorID string, h horario) int {
	var existentes struct {
		Items []struct {
			DiaSemana int    `json:"dia_semana"`
			Inicio    string `json:"inicio"`
		} `json:"items"`
	}
	pedir(token, http.MethodGet, "/activities/"+actividadID+"/schedules", nil, "", &existentes)
	creados := 0
	for _, dia := range h.dias {
		ya := false
		for _, e := range existentes.Items {
			if e.DiaSemana == dia && e.Inicio == h.inicio {
				ya = true
			}
		}
		if ya {
			continue
		}
		pedir(token, http.MethodPost, "/activities/"+actividadID+"/schedules", map[string]any{"dia_semana": dia, "inicio": h.inicio, "duracion_minutos": h.duracion, "profesor_id": profesorID, "capacidad": h.capacidad}, "", nil)
		creados++
	}
	return creados
}

// activar toma el enlace de activación del correo en Mailpit y elige una
// contraseña aleatoria para la alumna.
func activar(email string, desde time.Time) string {
	var token string
	for i := 0; i < 30 && token == ""; i++ {
		token = tokenActivacion(email, desde)
		if token == "" {
			time.Sleep(time.Second)
		}
	}
	if token == "" {
		salir(fmt.Errorf("sin correo"), "no llegó el correo de activación a Mailpit")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	password := "Demo-" + hex.EncodeToString(b)
	pedir("", http.MethodPost, "/auth/activate", map[string]any{"token": token, "password": password}, "", nil)
	return password
}

// tokenActivacion busca el enlace en el correo más reciente para email llegado
// después de desde: Mailpit puede conservar correos de cargas anteriores.
func tokenActivacion(email string, desde time.Time) string {
	var lista struct {
		Messages []struct {
			ID      string
			Created time.Time
			To      []struct{ Address string }
		}
	}
	if !getJSON(mailpit+"/api/v1/messages", &lista) {
		return ""
	}
	for _, m := range lista.Messages {
		if m.Created.Before(desde) {
			continue
		}
		for _, to := range m.To {
			if to.Address != email {
				continue
			}
			var detalle struct{ Text string }
			if !getJSON(mailpit+"/api/v1/message/"+m.ID, &detalle) {
				return ""
			}
			for _, linea := range strings.Split(detalle.Text, "\n") {
				linea = strings.TrimSpace(linea)
				if u, err := url.Parse(linea); err == nil && strings.HasPrefix(linea, "http") {
					if v, err := url.ParseQuery(u.Fragment); err == nil && v.Get("activate") != "" {
						return v.Get("activate")
					}
				}
			}
		}
	}
	return ""
}

func getJSON(u string, out any) bool {
	res, err := client.Get(u)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == 200 && json.NewDecoder(res.Body).Decode(out) == nil
}

func pedir(token, method, path string, body any, clave string, out any) {
	status := pedirEstado(token, method, path, body, clave, out)
	if status >= 300 {
		salir(fmt.Errorf("HTTP %d", status), method+" "+path)
	}
}

func pedirEstado(token, method, path string, body any, clave string, out any) int {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, gateway+path, reader)
	salir(err, "solicitud inválida")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		if clave == "" {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			clave = "demo-" + hex.EncodeToString(b)
		}
		req.Header.Set("Idempotency-Key", clave)
	}
	res, err := client.Do(req)
	salir(err, "el gateway no responde en localhost:8080; ¿está levantado el stack?")
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 && res.StatusCode != 409 {
		fmt.Fprintf(os.Stderr, "%s %s → %d: %s\n", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && res.StatusCode < 300 && len(raw) > 0 {
		salir(json.Unmarshal(raw, out), "respuesta inesperada de "+path)
	}
	return res.StatusCode
}

func salir(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", msg, err)
		os.Exit(1)
	}
}
