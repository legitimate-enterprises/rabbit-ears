/** Test fixture id n in the server's `uid.N(n)` shape. */
export function uid(n: number): string {
  return `00000000-0000-7000-8000-${n.toString(16).padStart(12, "0")}`;
}

/** Which of n colors an id shows: its last byte modulo n, as the server and the other clients pick. */
export const tint = (id: string, n: number) => parseInt(id.slice(-2), 16) % n;
