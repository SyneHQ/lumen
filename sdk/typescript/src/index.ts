/**
 * Official TypeScript/JavaScript client SDK for the Lumen Event Ingestion Service.
 */

export interface LumenOptions {
  endpoint?: string;
  batchSize?: number;
  flushIntervalMs?: number;
}

export type EventProps = Record<string, any>;

export class Lumen {
  private ingestKey: string;
  private endpoint: string;
  private batchSize: number;
  private flushIntervalMs: number;

  private anonId: string;
  private userId: string = '';
  private sessionId: string;
  private lastActivityAt: number;
  private sessionStartAt: number;

  private queue: Array<any> = [];
  private timer: any = null;

  constructor(ingestKey: string, options?: LumenOptions) {
    if (!ingestKey) {
      throw new Error('Lumen: ingestKey is required');
    }
    this.ingestKey = ingestKey;
    this.endpoint = options?.endpoint || 'http://localhost:50051';
    this.batchSize = options?.batchSize || 50;
    this.flushIntervalMs = options?.flushIntervalMs || 5000;

    const now = Date.now();
    this.lastActivityAt = now;
    this.sessionStartAt = now;

    // Load or generate anon_id from localStorage
    this.anonId = this.getOrInitAnonId();
    this.sessionId = this.generateUUIDv7();

    this.setupAutoFlush();
  }

  /**
   * Records a user event. Non-blocking & non-throwing.
   */
  public track(name: string, props?: EventProps): void {
    try {
      if (!name) return;

      this.checkSessionRotation();

      const event = {
        event_id: this.generateUUIDv7(),
        ts_unix_ms: Date.now(),
        name,
        props_json: this.toBase64(JSON.stringify(props || {})),
        overrides: {
          anon_id: this.anonId,
          user_id: this.userId,
          session_id: this.sessionId,
          sdk: 'typescript',
          sdk_version: '1.0.0',
          url: typeof window !== 'undefined' ? window.location.href : '',
          referrer: typeof document !== 'undefined' ? document.referrer : '',
          user_agent: typeof navigator !== 'undefined' ? navigator.userAgent : '',
        },
      };

      if (this.queue.length < 1000) {
        this.queue.push(event);
      }

      if (this.queue.length >= this.batchSize) {
        this.flush();
      }
    } catch {
      // Non-blocking fail-safe error boundary
    }
  }

  /**
   * Links an anonymous visitor to an authenticated user ID.
   */
  public identify(userId: string, traits?: EventProps): void {
    try {
      if (!userId) return;
      this.userId = userId;
      this.checkSessionRotation();

      const payload = {
        anon_id: this.anonId,
        user_id: this.userId,
        traits_json: this.toBase64(JSON.stringify(traits || {})),
      };

      this.sendPayload('/lumen.v1.IngestService/Identify', payload);
    } catch {
      // Non-blocking fail-safe
    }
  }

  /**
   * Resets identity state (e.g. on user logout).
   */
  public reset(): void {
    this.userId = '';
    this.anonId = this.generateUUIDv7();
    this.sessionId = this.generateUUIDv7();
    const now = Date.now();
    this.lastActivityAt = now;
    this.sessionStartAt = now;

    if (typeof localStorage !== 'undefined') {
      try {
        localStorage.setItem('lumen_anon_id', this.anonId);
      } catch {
        // localStorage fallback
      }
    }
  }

  /**
   * Flushes all queued events to the server.
   */
  public flush(): void {
    if (this.queue.length === 0) return;

    const eventsToSend = [...this.queue];
    this.queue = [];

    const payload = {
      context: {
        sdk: 'typescript',
        sdk_version: '1.0.0',
        anon_id: this.anonId,
        user_id: this.userId,
        session_id: this.sessionId,
      },
      events: eventsToSend,
    };

    this.sendPayload('/lumen.v1.IngestService/Track', payload);
  }

  private sendPayload(path: string, body: any): void {
    const url = `${this.endpoint}${path}`;
    const headers = {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      'x-lumen-key': this.ingestKey,
    };

    if (typeof fetch !== 'undefined') {
      fetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        keepalive: true,
      }).catch(() => {});
    }
  }

  private setupAutoFlush(): void {
    if (typeof window !== 'undefined') {
      this.timer = setInterval(() => this.flush(), this.flushIntervalMs);

      // Flush on page unload / hidden
      document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'hidden') {
          this.flush();
        }
      });
    }
  }

  private checkSessionRotation(): void {
    const now = Date.now();
    const thirtyMinMs = 30 * 60 * 1000;
    const twentyFourHoursMs = 24 * 60 * 60 * 1000;

    if (now - this.lastActivityAt > thirtyMinMs || now - this.sessionStartAt > twentyFourHoursMs) {
      this.sessionId = this.generateUUIDv7();
      this.sessionStartAt = now;
    }
    this.lastActivityAt = now;
  }

  private getOrInitAnonId(): string {
    if (typeof localStorage !== 'undefined') {
      try {
        const stored = localStorage.getItem('lumen_anon_id');
        if (stored) return stored;
        const newId = this.generateUUIDv7();
        localStorage.setItem('lumen_anon_id', newId);
        return newId;
      } catch {
        return this.generateUUIDv7();
      }
    }
    return this.generateUUIDv7();
  }

  /** Encodes a UTF-8 string to base64, as required for proto `bytes` fields in Connect's JSON codec. */
  private toBase64(str: string): string {
    if (typeof Buffer !== 'undefined') {
      return Buffer.from(str, 'utf-8').toString('base64');
    }
    return btoa(unescape(encodeURIComponent(str)));
  }

  private generateUUIDv7(): string {
    const timestamp = Date.now();
    const hexTs = timestamp.toString(16).padStart(12, '0');
    const rand = Math.floor(Math.random() * 0xffffffffffff).toString(16).padStart(12, '0');
    return `${hexTs.slice(0, 8)}-${hexTs.slice(8, 12)}-7000-8000-${rand}`;
  }
}
