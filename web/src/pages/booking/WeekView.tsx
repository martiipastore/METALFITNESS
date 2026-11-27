import { addDays, Clase, DAYS, localDate, range, SATURDAY, SATURDAY_CLOSE, SUNDAY, todayPlus } from './types';

// Agenda semanal de clases (pantalla de Clases, vista "Semana"). Los colores por
// actividad salen de la gama de la página: lima, oliva, amarillo, arena, salvia
// y oliva profundo; el rojo queda reservado para errores. La actividad y los
// lugares también se leen como texto, nunca solo por el color (DISENO §4).
export const ACTIVITY_TONES = 6;

/** Asigna a cada actividad un tono estable: el orden alfabético reparte los colores. */
export function activityTones(names: string[]): Map<string, number> {
  const sorted = [...new Set(names)].sort((a, b) => a.localeCompare(b, 'es'));
  return new Map(sorted.map((name, i) => [name, i % ACTIVITY_TONES]));
}

interface WeekViewProps {
  monday: string;
  classes: Clase[];
  tones: Map<string, number>;
  student: boolean;
  busy: string;
  pulses: Record<string, true>;
  onReserve: (c: Clase) => void;
}

export function WeekView({ monday, classes, tones, student, busy, pulses, onReserve }: WeekViewProps) {
  const today = todayPlus();
  const days = Array.from({ length: 7 }, (_, i) => addDays(monday, i));
  const byDay = new Map<string, Clase[]>(days.map((d) => [d, []]));
  for (const c of classes) byDay.get(localDate(c.inicio))?.push(c);
  for (const list of byDay.values()) list.sort((a, b) => a.inicio.localeCompare(b.inicio));

  return (
    <ol className="week-grid" aria-label="Agenda de la semana">
      {days.map((date, i) => {
        const list = byDay.get(date) ?? [];
        const [, month, dayNumber] = date.split('-');
        const closed = i + 1 === SUNDAY && list.length === 0;
        return (
          <li key={date} className={`week-day${date === today ? ' week-day-today' : ''}${date < today ? ' week-day-past' : ''}${closed ? ' week-day-closed' : ''}`}>
            <h2>
              <span>{DAYS[i + 1]}</span>
              <time dateTime={date}>{dayNumber}/{month}</time>
              {date === today && <b>Hoy</b>}
            </h2>
            {i + 1 === SATURDAY && <p className="week-hours">Abierto hasta las {SATURDAY_CLOSE}</p>}
            {closed ? (
              <p className="week-closed">Cerrado</p>
            ) : list.length === 0 ? (
              <p className="week-empty">Sin clases</p>
            ) : (
              <ul>
                {list.map((c) => <WeekBlock key={c.id} clase={c} tone={tones.get(c.actividad.nombre) ?? 0} student={student} busy={busy} pulse={pulses[c.id] === true} onReserve={() => onReserve(c)} />)}
              </ul>
            )}
          </li>
        );
      })}
    </ol>
  );
}

function WeekBlock({ clase: c, tone, student, busy, pulse, onReserve }: { clase: Clase; tone: number; student: boolean; busy: string; pulse: boolean; onReserve: () => void }) {
  const ratio = c.capacidad > 0 ? Math.min(1, c.ocupacion / c.capacidad) : 1;
  const full = c.disponibles === 0 && c.estado === 'PROGRAMADA';
  const status = c.estado === 'CANCELADA' ? 'Cancelada' : c.reservada_por_mi ? 'Reservada' : full ? 'Completa' : c.estado !== 'PROGRAMADA' ? 'Cerrada' : `${c.disponibles} libres`;
  return (
    <li className={`week-block week-tone-${tone}${c.estado === 'CANCELADA' ? ' week-block-cancelled' : ''}${c.reservada_por_mi ? ' week-block-mine' : ''}${pulse ? ' live-pulse' : ''}`}>
      <span className="week-block-time">{range(c)}</span>
      <strong>{c.actividad.nombre}</strong>
      <span className="week-block-teacher">{c.profesor.nombre}</span>
      <span className="week-block-occupancy" aria-label={`${c.ocupacion} de ${c.capacidad} lugares ocupados`}>
        <i style={{ transform: `scaleX(${ratio})` }} />
      </span>
      <span className="week-block-status">{c.reservada_por_mi ? '✓ ' : ''}{status}<small>{c.ocupacion}/{c.capacidad}</small></span>
      {student ? (
        c.reservable && <button type="button" disabled={busy !== ''} onClick={onReserve}>{busy === c.id ? 'Reservando…' : 'Reservar'}</button>
      ) : (
        <a href={`#/admin/clases/${c.id}`}>Ver detalle</a>
      )}
    </li>
  );
}

/** Leyenda de colores por actividad. */
export function WeekLegend({ tones }: { tones: Map<string, number> }) {
  if (tones.size === 0) return null;
  return (
    <ul className="week-legend" aria-label="Colores por actividad">
      {[...tones.entries()].map(([name, tone]) => <li key={name} className={`week-tone-${tone}`}><i aria-hidden="true" />{name}</li>)}
    </ul>
  );
}
