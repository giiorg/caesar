// NOT A TEST. A fixture, deliberately named `.test.tsx`, holding one of every
// form the token checker reports — so that if the exclusion in tokens.ts is
// ever dropped, this file's violations reappear and arch.test.ts goes red.
//
// vitest.config.ts excludes `tools/**/fixtures/**` from the `node` project, so
// it is never collected as a suite. That exclusion already exists for
// `groups/routes/a/helper.test.ts` and the reasoning is recorded there.
export function Bad() {
  return (
    <div className="bg-white text-red-500">
      <span style={{ color: 'rebeccapurple' }} />
      <span className="bg-[#ff0000]" />
    </div>
  )
}
