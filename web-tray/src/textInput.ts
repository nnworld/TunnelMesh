/**
 * Attributes that stop the platform from rewriting what the operator typed.
 *
 * Every field this is applied to holds something that has to be byte-exact: a server URL, a
 * token, a listen address, an agent target. macOS text assists (auto-capitalisation, spelling
 * correction, text substitution) and the browser's own autofill all change characters after
 * the fact, and each of them turns a saved route into one that silently fails to connect.
 * The tray window also registers the matching NSUserDefaults overrides on the native side;
 * these attributes are what the web view itself can honour.
 */
export const technicalInput = {
  autocapitalize: 'none',
  autocorrect: 'off',
  spellcheck: 'false',
  autocomplete: 'off',
} as const
