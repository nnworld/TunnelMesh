// zmodem.js (Apache-2.0) ships CommonJS without type declarations. Only the
// surface used by ./zmodem.ts is described here; everything else stays unknown
// so a library upgrade cannot silently change our assumptions.
declare module 'zmodem.js/src/zmodem_browser' {
  export type ZmodemSessionType = 'send' | 'receive'

  export interface ZmodemFileDetails {
    name: string
    size: number
    mtime?: Date
    mode?: number | null
    files_remaining?: number
    bytes_remaining?: number
  }

  export interface ZmodemTransfer {
    send(chunk: Uint8Array | number[]): void
    end(chunk?: Uint8Array | number[]): Promise<void>
    get_offset(): number
    get_details(): ZmodemFileDetails
  }

  export interface ZmodemOffer {
    accept(): Promise<Uint8Array[]>
    skip(): void
    on(event: 'input', handler: (payload: Uint8Array) => void): void
    get_details(): ZmodemFileDetails
    get_offset(): number
  }

  export interface ZmodemSession {
    type: ZmodemSessionType
    start(): void
    abort(): void
    aborted(): boolean
    has_ended(): boolean
    close(): Promise<void>
    consume(octets: number[]): void
    set_sender(sender: (octets: number[]) => void): void
    on(event: string, handler: (...args: never[]) => void): void
    send_offer(details: ZmodemFileDetails): Promise<ZmodemTransfer | undefined>
  }

  export interface ZmodemDetection {
    confirm(): ZmodemSession
    deny(): void
    is_valid(): boolean
    get_session_role(): ZmodemSessionType
  }

  export interface ZmodemSentryOptions {
    to_terminal(octets: number[]): void
    sender(octets: number[]): void
    on_detect(detection: ZmodemDetection): void
    on_retract(): void
  }

  export class Sentry {
    constructor(options: ZmodemSentryOptions)
    consume(input: ArrayLike<number> | ArrayBuffer): void
    get_confirmed_session(): ZmodemSession | null
  }

  export const Browser: {
    send_files(session: ZmodemSession, files: File[] | FileList, options?: {
      on_offer_response?(file: File, transfer?: ZmodemTransfer): void
      on_progress?(file: File, transfer: ZmodemTransfer, chunk: Uint8Array): void
      on_file_complete?(file: File, transfer: ZmodemTransfer): void
    }): Promise<void>
    save_to_disk(packets: BlobPart[], name: string): void
  }

  export const Header: {
    build(name: string, args?: unknown): { to_hex(): number[] }
    parse_hex(octets: ArrayLike<number>): unknown
  }

  export const Session: {
    Send: new (zrinitHeader: unknown) => ZmodemSession
    Receive: new () => ZmodemSession
    parse(octets: ArrayLike<number>): ZmodemSession | undefined
  }

  const Zmodem: {
    Sentry: typeof Sentry
    Browser: typeof Browser
    Header: typeof Header
    Session: typeof Session
    DEBUG: boolean
  }
  export default Zmodem
}
