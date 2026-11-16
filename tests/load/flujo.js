// Carga del flujo principal del gimnasio (D13, D-97): búsqueda de clases y
// reservas a través del gateway, con las dos instancias de Booking.
//
// Lo ejecuta tests/load/run-flujo.go dentro de la red de Docker; no se corre a
// mano. Variables:
//   ESCENARIO  busqueda | pico | mezcla
//   VUS        usuarios simultáneos
//   CALENTAMIENTO, MEDICION  duraciones (busqueda y mezcla)
//   /datos/tokens.json    [jwt, ...]  sesiones de los alumnos sintéticos; las
//                         abre el runner una sola vez (Members limita el login)
//   /datos/clases.json    {pico: "<id>", pool: ["<id>", ...], desde, hasta}
//
// Solo cuenta como error un 5xx, un 429, un 401/403 o un timeout. Un 409 en una
// reserva (SIN_CUPO, RESERVA_DUPLICADA, SUPERPOSICION_RESERVA) es una respuesta
// de negocio correcta y se cuenta aparte.
import http from 'k6/http';
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://api-gateway:8080/api/v1';
const ESCENARIO = __ENV.ESCENARIO;
const VUS = Number(__ENV.VUS || 10);
const tokens = JSON.parse(open('/datos/tokens.json'));
const clases = JSON.parse(open('/datos/clases.json'));

// Métricas solo de la ventana de medición (sin calentamiento).
const duracionBusqueda = new Trend('medicion_busqueda_ms', true);
const duracionReserva = new Trend('medicion_reserva_ms', true);
const operaciones = new Counter('medicion_operaciones');
const errores = new Counter('medicion_errores');
const reservasConfirmadas = new Counter('reservas_confirmadas');
const reservasRechazadas = new Counter('reservas_negocio_409');

function escenarios() {
  if (ESCENARIO === 'pico') {
    // Ráfaga: cada usuario intenta reservar una vez la misma clase.
    return { medicion: { executor: 'per-vu-iterations', vus: VUS, iterations: 1, maxDuration: '60s' } };
  }
  return {
    calentamiento: { executor: 'constant-vus', vus: VUS, duration: __ENV.CALENTAMIENTO || '30s' },
    medicion: { executor: 'constant-vus', vus: VUS, duration: __ENV.MEDICION || '60s', startTime: __ENV.CALENTAMIENTO || '30s' },
  };
}

export const options = {
  scenarios: escenarios(),
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  // Sin estas referencias k6 no exporta los contadores en el resumen.
  // Lo mismo con el desglose de errores por código: cada submétrica necesita su
  // propio umbral para aparecer.
  thresholds: Object.fromEntries([
    ['medicion_operaciones', ['count>=0']], ['medicion_errores', ['count>=0']],
    ...['0', '400', '401', '403', '404', '422', '429', '500', '502', '503', '504'].map((s) => [`medicion_errores{status:${s}}`, ['count>=0']]),
  ]),
};

const textos = ['', '', 'zum', 'fun', 'gap', 'str', 'mus', 'gom', 'ruiz', 'yo', 'pil'];

function medir(r, tipo) {
  if (exec.scenario.name !== 'medicion') return;
  operaciones.add(1);
  (tipo === 'reserva' ? duracionReserva : duracionBusqueda).add(r.timings.duration);
  const negocio = tipo === 'reserva' && r.status === 409;
  if (r.status === 0 || r.status >= 500 || r.status === 429 || r.status === 401 || r.status === 403 || (r.status >= 400 && !negocio)) {
    errores.add(1, { status: String(r.status) });
  }
  if (tipo === 'reserva') {
    if (r.status === 201) reservasConfirmadas.add(1);
    if (negocio) reservasRechazadas.add(1);
  }
}

function buscar(token) {
  const q = textos[Math.floor(Math.random() * textos.length)];
  const disp = Math.random() < 0.3 ? '&solo_disponibles=true' : '';
  const r = http.get(`${BASE}/classes?fecha_desde=${clases.desde}&fecha_hasta=${clases.hasta}&pagina=1&tamano=20${q ? `&q=${q}` : ''}${disp}`,
    { headers: { Authorization: `Bearer ${token}` }, tags: { op: 'busqueda' }, timeout: '10s' });
  medir(r, 'busqueda');
}

function reservar(token, claseId) {
  const r = http.post(`${BASE}/bookings`, JSON.stringify({ clase_id: claseId }), {
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', 'Idempotency-Key': `carga-${exec.vu.idInTest}-${exec.scenario.iterationInTest}-${Date.now()}` },
    tags: { op: 'reserva' }, timeout: '10s',
  });
  medir(r, 'reserva');
}

export default function () {
  const token = tokens[(exec.vu.idInTest - 1) % tokens.length];
  if (ESCENARIO === 'pico') {
    reservar(token, clases.pico);
  } else if (ESCENARIO === 'mezcla' && Math.random() < 0.1) {
    reservar(token, clases.pool[Math.floor(Math.random() * clases.pool.length)]);
  } else {
    buscar(token);
  }
}

// El runner lee el resumen completo; no se imprime nada por consola.
export function handleSummary(data) {
  return { '/datos/resumen.json': JSON.stringify(data) };
}
