import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSession } from '../../auth/session';
import { useConfirm } from '../../components/ConfirmDialog';
import { EmptyState, Notice, NoticeState, PageHeading } from '../../components/PageParts';
import { CambioOcupacion, EstadoEnVivo, useCuposEnVivo } from './live';
import { Actividad, addDays, Clase, CLASS_STATES, day, errorText, mondayOf, newIdempotencyKey, occupancy, PaginaClases, range, time, todayPlus } from './types';
import { activityTones, WeekLegend, WeekView } from './WeekView';

// Clases disponibles y reserva (booking.v1, HU-13 y HU-14). El administrador usa
// la misma búsqueda y entra al detalle de cada clase. La reserva se valida
// siempre en el backend.
const PAGE_SIZE = 20;
// Espera desde la última tecla antes de buscar: "zum" consulta una vez, no tres.
const TYPING_DELAY_MS = 300;
// La lista se vuelve a consultar sola: un cambio de cupos o una clase nueva del
// administrador aparece sin que el alumno tenga que buscar de nuevo.
const REFRESH_MS = 30_000;
// Cuánto dura el resaltado de una clase que cambió en vivo.
const PULSE_MS = 1_400;
// Máximo que acepta la API por página; la semana se pide completa.
const WEEK_PAGE_SIZE = 100;
const VIEW_KEY = 'metalfitness.clases.vista';

type Vista = 'lista' | 'semana';

function storedView(): Vista {
  try {
    return window.localStorage.getItem(VIEW_KEY) === 'semana' ? 'semana' : 'lista';
  } catch {
    return 'lista';
  }
}

/** "16/11 – 22/11" para el encabezado de la semana. */
function weekLabel(monday: string): string {
  const fmt = (d: string) => `${d.slice(8, 10)}/${d.slice(5, 7)}`;
  return `${fmt(monday)} – ${fmt(addDays(monday, 6))}`;
}

/** Aplica un cambio en vivo si es más nuevo que lo que ya muestra la lista. */
function aplicarCambio(c: Clase, cambio: CambioOcupacion): Clase {
  const disponibles = Math.max(0, cambio.capacidad - cambio.ocupacion);
  const futura = Date.parse(c.inicio) > Date.now();
  return {
    ...c,
    ocupacion: cambio.ocupacion,
    capacidad: cambio.capacidad,
    estado: cambio.estado,
    disponibles,
    reservable: !c.reservada_por_mi && cambio.estado === 'PROGRAMADA' && futura && disponibles > 0,
  };
}

export function ClassesPage() {
  const { authRequest, user } = useSession();
  const [dialog, confirm] = useConfirm();
  const [activities, setActivities] = useState<Actividad[]>([]);
  const initial = { texto: '', desde: todayPlus(), hasta: todayPlus(7), actividad: '', soloDisponibles: false };
  const [filters, setFilters] = useState(initial);
  const [applied, setApplied] = useState(initial);
  const [page, setPage] = useState(1);
  const [data, setData] = useState<PaginaClases | null>(null);
  const [notice, setNotice] = useState<NoticeState | null>(null);
  const [busy, setBusy] = useState('');
  // Vista "Semana": agenda de lunes a domingo con colores por actividad.
  const [vista, setVista] = useState<Vista>(storedView);
  const [monday, setMonday] = useState(() => mondayOf(todayPlus()));
  // Cupos en vivo (D-103): versión vista por clase y clases resaltadas.
  const versiones = useRef(new Map<string, number>());
  const [pulsos, setPulsos] = useState<Record<string, true>>({});

  useEffect(() => {
    authRequest<{ items: Actividad[] }>('/api/v1/activities')
      .then((res) => setActivities(res.data.items.filter((a) => a.activa)))
      .catch(() => setActivities([]));
  }, [authRequest]);

  const load = useCallback(async () => {
    const semana = vista === 'semana';
    const params = new URLSearchParams({
      fecha_desde: semana ? monday : applied.desde,
      fecha_hasta: semana ? addDays(monday, 6) : applied.hasta,
      pagina: String(semana ? 1 : page),
      tamano: String(semana ? WEEK_PAGE_SIZE : PAGE_SIZE),
    });
    if (applied.texto.trim()) params.set('q', applied.texto.trim());
    if (applied.actividad) params.set('actividad_id', applied.actividad);
    if (applied.soloDisponibles) params.set('solo_disponibles', 'true');
    try {
      const res = await authRequest<PaginaClases>(`/api/v1/classes?${params.toString()}`);
      if (!semana) {
        setData(res.data);
        return;
      }
      // La semana entera, aunque tenga más clases que una página.
      const items = [...res.data.items];
      for (let pagina = 2; items.length < res.data.total && pagina <= 10; pagina++) {
        params.set('pagina', String(pagina));
        const more = await authRequest<PaginaClases>(`/api/v1/classes?${params.toString()}`);
        if (more.data.items.length === 0) break;
        items.push(...more.data.items);
      }
      setData({ ...res.data, items, pagina: 1, tamano: items.length });
    } catch (error) {
      setNotice({ tone: 'error', text: errorText(error, 'No se pudieron cargar las clases.') });
    }
  }, [authRequest, applied, page, vista, monday]);

  function changeView(next: Vista) {
    setVista(next);
    setPage(1);
    try {
      window.localStorage.setItem(VIEW_KEY, next);
    } catch {
      // sin almacenamiento local, la vista vuelve a "Lista" al recargar
    }
  }

  const tones = useMemo(() => activityTones([...activities.map((a) => a.nombre), ...(data?.items.map((c) => c.actividad.nombre) ?? [])]), [activities, data]);

  useEffect(() => {
    void load();
  }, [load]);

  const enVivo = useCuposEnVivo((cambio) => {
    if ((versiones.current.get(cambio.clase_id) ?? 0) >= cambio.version) return;
    versiones.current.set(cambio.clase_id, cambio.version);
    let visible = false;
    setData((prev) => {
      if (!prev || !prev.items.some((c) => c.id === cambio.clase_id)) return prev;
      visible = true;
      return { ...prev, items: prev.items.map((c) => (c.id === cambio.clase_id ? aplicarCambio(c, cambio) : c)) };
    });
    if (!visible) return;
    setPulsos((p) => ({ ...p, [cambio.clase_id]: true }));
    window.setTimeout(() => setPulsos((p) => {
      const { [cambio.clase_id]: _, ...resto } = p;
      return resto;
    }), PULSE_MS);
  }, () => void load());

  // Actualización sola: cada 30 s con la pestaña visible y al volver a ella.
  // Sigue como respaldo de los cupos en vivo.
  useEffect(() => {
    const refresh = () => {
      if (document.visibilityState === 'visible') void load();
    };
    const timer = window.setInterval(refresh, REFRESH_MS);
    document.addEventListener('visibilitychange', refresh);
    window.addEventListener('focus', refresh);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', refresh);
      window.removeEventListener('focus', refresh);
    };
  }, [load]);

  // Búsqueda mientras se escribe: el texto se aplica solo, sin tocar "Buscar".
  useEffect(() => {
    if (filters.texto === applied.texto) return;
    const timer = window.setTimeout(() => {
      setPage(1);
      setApplied((prev) => ({ ...prev, texto: filters.texto }));
    }, TYPING_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [filters.texto, applied.texto]);

  function search(event: FormEvent) {
    event.preventDefault();
    setNotice(null);
    setPage(1);
    setApplied(filters);
  }

  function clearFilters() {
    setNotice(null);
    setPage(1);
    setFilters(initial);
    setApplied(initial);
  }

  async function reserve(c: Clase) {
    setBusy(c.id);
    setNotice(null);
    try {
      await authRequest('/api/v1/bookings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': newIdempotencyKey() },
        body: JSON.stringify({ clase_id: c.id }),
      });
      setNotice({ tone: 'success', plate: true, text: `Reserva confirmada: ${c.actividad.nombre} del ${day(c.inicio)}, ${range(c)}.` });
      await load();
    } catch (error) {
      setNotice({ tone: 'error', text: errorText(error, 'No se pudo reservar.') });
    } finally {
      setBusy('');
    }
  }

  // Cupos de una clase (HU-08, RN-10): el backend rechaza bajarlos por debajo
  // de la ocupación o superar el tope de la actividad.
  async function changeCapacity(c: Clase) {
    const value = await confirm({
      title: `Cupos de ${c.actividad.nombre}`,
      message: `${day(c.inicio)}, ${range(c)}. Hay ${c.ocupacion} reservas: los cupos no pueden quedar por debajo de eso ni superar el tope de la actividad.`,
      confirmLabel: 'Guardar cupos',
      input: { label: 'Cupos', type: 'number', initial: String(c.capacidad), min: Math.max(1, c.ocupacion), max: 50 },
    });
    if (value === null) return;
    setBusy(c.id);
    setNotice(null);
    try {
      await authRequest(`/api/v1/classes/${c.id}/capacity`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ capacidad: Number(value) }),
      });
      setNotice({ tone: 'success', text: `Cupos actualizados: ${c.actividad.nombre} del ${day(c.inicio)} ahora tiene ${value}. Los alumnos lo ven en unos segundos.` });
      await load();
    } catch (error) {
      setNotice({ tone: 'error', text: errorText(error, 'No se pudieron cambiar los cupos.') });
    } finally {
      setBusy('');
    }
  }

  const student = user?.role === 'ALUMNO';
  const pages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1;
  const activeName = activities.find((a) => a.id === applied.actividad)?.nombre;
  const filtered = applied.texto.trim() !== '' || applied.actividad !== '' || applied.soloDisponibles || applied.desde !== initial.desde || applied.hasta !== initial.hasta;

  return (
    <main className="page admin-page">
      {dialog}
      {student ? (
        <PageHeading eyebrow="Agenda de entrenamiento" title="Clases" description="Elegí actividad y fechas, mirá cuántos lugares quedan y reservá en un paso." index="02" />
      ) : (
        <PageHeading eyebrow="Agenda operativa" title="Clases" description="Consultá la programación, revisá la ocupación y abrí el detalle de cada clase." index="03" />
      )}
      <form className="filters admin-toolbar-panel" onSubmit={search} aria-label="Filtros de clases">
        <label className="filters-search">Buscar
          <input type="search" placeholder="Ej.: zum, funcional o el nombre del profesor" maxLength={60} value={filters.texto} onChange={(e) => setFilters({ ...filters, texto: e.target.value })} />
        </label>
        {vista === 'lista' && <>
          <label>Desde <input type="date" value={filters.desde} onChange={(e) => setFilters({ ...filters, desde: e.target.value })} required /></label>
          <label>Hasta <input type="date" value={filters.hasta} onChange={(e) => setFilters({ ...filters, hasta: e.target.value })} required /></label>
        </>}
        <label>Actividad
          <select value={filters.actividad} onChange={(e) => setFilters({ ...filters, actividad: e.target.value })}>
            <option value="">Todas</option>
            {activities.map((a) => <option key={a.id} value={a.id}>{a.nombre}</option>)}
          </select>
        </label>
        <label className="check"><input type="checkbox" checked={filters.soloDisponibles} onChange={(e) => setFilters({ ...filters, soloDisponibles: e.target.checked })} /> Solo con lugar</label>
        <button type="submit">Buscar</button>
      </form>
      {filtered && (
        <p className="active-filters">
          <span>Filtros activos:</span>
          {applied.texto.trim() && <b>“{applied.texto.trim()}”</b>}
          {vista === 'lista' && <b>{applied.desde} → {applied.hasta}</b>}
          {activeName && <b>{activeName}</b>}
          {applied.soloDisponibles && <b>Solo con lugar</b>}
          <button type="button" className="button-secondary" onClick={clearFilters}>Limpiar filtros</button>
        </p>
      )}
      <div className="view-bar">
        <div className="view-toggle" role="group" aria-label="Vista de las clases">
          <button type="button" aria-pressed={vista === 'lista'} onClick={() => changeView('lista')}>Lista</button>
          <button type="button" aria-pressed={vista === 'semana'} onClick={() => changeView('semana')}>Semana</button>
        </div>
        {vista === 'semana' && (
          <nav className="week-nav" aria-label="Semanas">
            <button type="button" className="button-secondary" onClick={() => setMonday(addDays(monday, -7))} aria-label="Semana anterior">←</button>
            <strong>{weekLabel(monday)}</strong>
            <button type="button" className="button-secondary" onClick={() => setMonday(addDays(monday, 7))} aria-label="Semana siguiente">→</button>
            {monday !== mondayOf(todayPlus()) && <button type="button" className="button-secondary" onClick={() => setMonday(mondayOf(todayPlus()))}>Esta semana</button>}
          </nav>
        )}
        <LiveStatus estado={enVivo} />
      </div>
      {vista === 'semana' && <WeekLegend tones={tones} />}
      <Notice key={notice?.text} notice={notice} />
      {data?.origen === 'cache_vencida' && (
        <Notice notice={{ tone: 'info', text: `Mostramos la última búsqueda guardada, de las ${time(data.consultado_en)}, porque el sistema de clases no responde. La reserva se confirma siempre con la disponibilidad real.` }} />
      )}
      {student && data?.marcas_disponibles === false && (
        <Notice notice={{ tone: 'info', text: 'No pudimos verificar tus reservas en este momento. Si ya reservaste una clase, el sistema no te deja duplicarla.' }} />
      )}
      {data === null ? (
        notice?.tone !== 'error' && <div className="dashboard-skeleton" aria-label="Cargando clases" />
      ) : vista === 'semana' ? (
        <WeekView monday={monday} classes={data.items} tones={tones} student={student} busy={busy} pulses={pulsos} onReserve={(c) => void reserve(c)} />
      ) : data.items.length === 0 ? (
        <EmptyState title="Sin clases para esos filtros">Probá con otro texto, otras fechas, otra actividad o sin “Solo con lugar”.</EmptyState>
      ) : student ? (
        <ol className="class-grid" aria-label="Clases encontradas">
          {data.items.map((c) => <ClassCard key={c.id} clase={c} busy={busy} pulse={pulsos[c.id] === true} onReserve={() => void reserve(c)} />)}
        </ol>
      ) : (
        <table className="data-table responsive-table">
          <thead>
            <tr><th>Día</th><th>Horario</th><th>Actividad</th><th>Profesor</th><th>Ocupación</th><th>Estado</th><th><span className="sr-only">Acciones</span></th></tr>
          </thead>
          <tbody>
            {data.items.map((c) => (
              <tr key={c.id} className={pulsos[c.id] ? 'live-pulse' : undefined}>
                <td data-label="Día">{day(c.inicio)}</td>
                <td data-label="Horario">{range(c)}</td>
                <td data-label="Actividad">{c.actividad.nombre}</td>
                <td data-label="Profesor">{c.profesor.nombre}</td>
                <td data-label="Ocupación">{occupancy(c)}</td>
                <td data-label="Estado">{CLASS_STATES[c.estado]}</td>
                <td>
                  {c.estado === 'PROGRAMADA' && <><button type="button" disabled={busy !== ''} onClick={() => void changeCapacity(c)}>{busy === c.id ? 'Guardando…' : 'Cupos'}</button>{' '}</>}
                  <a href={`#/admin/clases/${c.id}`}>Ver detalle</a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {data && vista === 'lista' && pages > 1 && (
        <nav className="pagination" aria-label="Páginas de clases">
          <button type="button" disabled={page <= 1} onClick={() => setPage(page - 1)}>Anterior</button>
          <span>Página {page} de {pages}</span>
          <button type="button" disabled={page >= pages} onClick={() => setPage(page + 1)}>Siguiente</button>
        </nav>
      )}
    </main>
  );
}

/** Indicador de la conexión en vivo; el texto lo leen los lectores de pantalla. */
function LiveStatus({ estado }: { estado: EstadoEnVivo }) {
  const texto = estado === 'en_vivo' ? 'Cupos en vivo' : estado === 'conectando' ? 'Conectando…' : 'Sin actualización en vivo: la lista se renueva cada 30 s';
  return (
    <p className={`live-status live-status-${estado}`} role="status" aria-live="polite">
      <i aria-hidden="true" />{texto}
    </p>
  );
}

function ClassCard({ clase: c, busy, pulse, onReserve }: { clase: Clase; busy: string; pulse: boolean; onReserve: () => void }) {
  const ratio = c.capacidad > 0 ? Math.min(1, c.ocupacion / c.capacidad) : 1;
  const full = c.disponibles === 0 && c.estado === 'PROGRAMADA';
  const tone = c.reservada_por_mi ? 'mine' : full ? 'full' : c.reservable ? (c.disponibles <= 3 ? 'few' : 'open') : 'closed';
  const status = c.reservada_por_mi ? 'Ya reservada' : full ? 'Completa' : c.reservable ? `${c.disponibles} ${c.disponibles === 1 ? 'lugar libre' : 'lugares libres'}` : 'No disponible';
  return (
    <li className={`class-card class-card-${tone}${pulse ? ' live-pulse' : ''}`}>
      <header>
        <time dateTime={c.inicio}><strong>{day(c.inicio)}</strong><span>{range(c)}</span></time>
        <span className="class-card-state">{c.reservada_por_mi ? '✓ ' : full ? '■ ' : ''}{status}</span>
      </header>
      <h2>{c.actividad.nombre}</h2>
      <p>Profesor: {c.profesor.nombre}</p>
      <div className="occupancy" aria-label={occupancy(c)}>
        <span className="occupancy-track"><i style={{ transform: `scaleX(${ratio})` }} /></span>
        <small>{c.ocupacion}/{c.capacidad} ocupados</small>
      </div>
      {c.reservada_por_mi ? (
        <a className="class-card-link" href="#/app/reservas">Ver en mis reservas →</a>
      ) : c.reservable ? (
        <button type="button" disabled={busy !== ''} onClick={onReserve}>{busy === c.id ? 'Reservando…' : 'Reservar'}</button>
      ) : (
        <button type="button" disabled>{full ? 'Sin lugar' : CLASS_STATES[c.estado]}</button>
      )}
    </li>
  );
}
