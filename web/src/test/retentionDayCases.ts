// Test data (#406): the end of a period, 1 January 2026 00:00 in the rule's
// time zone, as the server sends it.
export const periodEnds: [zone: string, end: string][] = [
  ["Europe/Berlin", "2025-12-31T23:00:00Z"],
  ["America/New_York", "2026-01-01T05:00:00Z"],
  ["Pacific/Pago_Pago (-11)", "2026-01-01T11:00:00Z"],
  ["Asia/Tokyo", "2025-12-31T15:00:00Z"],
  ["Pacific/Kiritimati (+14)", "2025-12-31T10:00:00Z"],
];
