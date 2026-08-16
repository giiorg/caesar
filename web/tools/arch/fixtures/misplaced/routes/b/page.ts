// The second consumer. helper.ts must now live at routes/, not routes/a/.
import { shared } from '../a/helper'
export const b = shared
