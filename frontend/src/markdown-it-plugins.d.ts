// Both plugins ship no type declarations; declare the minimal surface we use.
// Ambient module declarations must live in a global (non-module) d.ts — no
// top-level imports here — so the md parameter is left structural.
declare module 'markdown-it-task-lists' {
  interface TaskListOptions {
    /** Make checkboxes clickable (they are read-only in the preview). */
    enabled?: boolean
    label?: boolean
    labelAfter?: boolean
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const taskLists: (md: any, options?: TaskListOptions) => void
  export default taskLists
}

declare module 'markdown-it-footnote' {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const footnote: (md: any) => void
  export default footnote
}
