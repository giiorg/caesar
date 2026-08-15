// A test is NOT a consumer. If this file counts, every single-consumer module
// with a test would be reported and the rule would be noise.
import { shared } from './helper'
export const t = shared
