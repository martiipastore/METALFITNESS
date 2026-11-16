import { useEffect, useRef, useState } from 'react';
import { useSession } from '../../auth/session';

/** Cambio de ocupación que publica Booking (D-103), sin datos de personas. */
export interface CambioOcupacion {
  clase_id: string;
  ocupacion: number;
  capacidad: number;
  estado: 'PROGRAMADA' | 'EN_CURSO' | 'FINALIZADA' | 'CANCELADA';
  version: number;
}

export type EstadoEnVivo = 'conectando' | 'en_vivo' | 'sin_conexion';

/**
 * Cupos en vivo (bonus D-40, D-103): mantiene abierta la suscripción a
 * /api/v1/classes/stream y llama a onCambio por cada clase que cambia. Si la
 * conexión se corta, reintenta con espera creciente (2 s a 30 s) y llama a
 * onReconectar para resincronizar la lista. Con la pestaña oculta se desconecta.
 */
export function useCuposEnVivo(onCambio: (c: CambioOcupacion) => void, onReconectar: () => void): EstadoEnVivo {
  const { authStream } = useSession();
  const [estado, setEstado] = useState<EstadoEnVivo>('conectando');
  const cambio = useRef(onCambio);
  const reconectar = useRef(onReconectar);
  cambio.current = onCambio;
  reconectar.current = onReconectar;

  useEffect(() => {
    let activo = true;
    let control: AbortController | null = null;
    let espera = 2000;
    let temporizador = 0;
    let primera = true;

    const conectar = () => {
      if (!activo || document.visibilityState !== 'visible') return;
      control = new AbortController();
      setEstado('conectando');
      const actual = control;
      authStream('/api/v1/classes/stream', (evento, data) => {
        if (evento !== 'ocupacion') return;
        try {
          cambio.current(JSON.parse(data) as CambioOcupacion);
        } catch {
          // un evento ilegible se ignora: la lista se resincroniza igual
        }
      }, actual.signal)
        .then(() => undefined, () => undefined)
        .finally(() => {
          if (!activo || actual.signal.aborted) return;
          setEstado('sin_conexion');
          temporizador = window.setTimeout(conectar, espera);
          espera = Math.min(espera * 2, 30_000);
        });
      // El stream no confirma la conexión con un evento: se da por abierta si
      // sigue viva un instante después. Al reconectar se resincroniza la lista.
      window.setTimeout(() => {
        if (!activo || actual.signal.aborted || control !== actual) return;
        setEstado('en_vivo');
        espera = 2000;
        if (!primera) reconectar.current();
        primera = false;
      }, 800);
    };

    const alCambiarVisibilidad = () => {
      if (document.visibilityState === 'visible') {
        window.clearTimeout(temporizador);
        if (!control || control.signal.aborted) conectar();
      } else {
        control?.abort();
        control = null;
        setEstado('sin_conexion');
      }
    };

    conectar();
    document.addEventListener('visibilitychange', alCambiarVisibilidad);
    return () => {
      activo = false;
      window.clearTimeout(temporizador);
      control?.abort();
      document.removeEventListener('visibilitychange', alCambiarVisibilidad);
    };
  }, [authStream]);

  return estado;
}
