// Offline hints. Online, the server works out the easiest next step from
// the board as it stands (POST /api/hint). Offline, this file answers from
// the solving path the grader recorded when it made the puzzle.
//
// Every step on that path is a true deduction, so an offline hint is never
// wrong. It can be less helpful: the path follows the grader's order, not
// the player's, so the step it offers may not be the easiest one on the
// board right now.

import { wrongCells } from "./game.js";

/**
 * Returns a hint in the server's shape: { mistakes } or { steps }.
 * @param {object} g the game document
 */
export function offlineHint(g) {
  const mistakes = wrongCells(g);
  if (mistakes.length) return { mistakes };

  // Walk the path. Eliminations collect until the next placement; if that
  // placement's cell is still empty, those eliminations are what lead to it.
  let pending = [];
  for (const step of g.steps ?? []) {
    if (!step.place) {
      pending.push(step);
      continue;
    }
    if (g.entries[step.place.cell] === 0) return { steps: [...pending, step] };
    pending = [];
  }
  return { steps: [] };
}
