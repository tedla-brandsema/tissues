import {
  provideFluentDesignSystem,
  fluentButton,
  fluentCard,
  fluentTextField,
} from "@fluentui/web-components";

provideFluentDesignSystem().register(
  fluentButton(),
  fluentCard(),
  fluentTextField()
);

export class TissuesAuthLogin extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this.state = {
      loading: false,
      error: "",
    };
  }

  connectedCallback() {
    this.render();
  }

  basePath() {
    const base = this.getAttribute("base") || "";
    if (base === "/") return "";
    return base;
  }

  nextParam() {
    try {
      const params = new URLSearchParams(window.location.search);
      return params.get("next") || "";
    } catch {
      return "";
    }
  }

  queryParam(name) {
    try {
      const params = new URLSearchParams(window.location.search);
      return params.get(name) || "";
    } catch {
      return "";
    }
  }

  setState(partial) {
    this.state = { ...this.state, ...partial };
    this.render();
  }

  submit() {
    const emailInput = this.shadowRoot.getElementById("email-input");
    const passwordInput = this.shadowRoot.getElementById("password-input");

    const email = (emailInput?.value || "").trim();
    const password = passwordInput?.value || "";

    if (!email || !password) {
      this.setState({ error: "Email and password are required." });
      return;
    }

    this.setState({ loading: true, error: "" });

    const next = this.nextParam();
    const nextExp = this.queryParam("next_exp");
    const nextSig = this.queryParam("next_sig");

    const form = this.shadowRoot.getElementById("login-form");
    const emailField = this.shadowRoot.getElementById("email-field");
    const passwordField = this.shadowRoot.getElementById("password-field");
    const nextField = this.shadowRoot.getElementById("next-field");
    const nextExpField = this.shadowRoot.getElementById("next-exp-field");
    const nextSigField = this.shadowRoot.getElementById("next-sig-field");
    const submitBtn = this.shadowRoot.getElementById("submit-btn");
    if (!form) {
      this.setState({ loading: false, error: "Sign in failed." });
      return;
    }

    if (emailField) emailField.value = email;
    if (passwordField) passwordField.value = password;
    if (nextField) nextField.value = next;
    if (nextExpField) nextExpField.value = nextExp;
    if (nextSigField) nextSigField.value = nextSig;

    if (submitBtn) submitBtn.textContent = "Signing in...";
    form.submit();
  }

  render() {
    const { loading, error } = this.state;

    this.shadowRoot.innerHTML = `
      <style>
        :host {
          display: block;
          font-family: "Segoe UI Variable Display", "Segoe UI", system-ui, sans-serif;
          min-height: 100vh;
          background: radial-gradient(circle at top, #f2f7ff, #f8f9fc 60%, #ffffff 100%);
          padding: 40px 16px;
          color: #1b1b1b;
        }

        .wrap {
          max-width: 420px;
          margin: 0 auto;
        }

        .brand {
          font-size: 14px;
          letter-spacing: 0.16em;
          text-transform: uppercase;
          color: #5b5b5b;
          margin-bottom: 10px;
        }

        .title {
          font-size: 28px;
          font-weight: 600;
          margin: 0 0 8px 0;
        }

        .subtitle {
          font-size: 14px;
          color: #5b5b5b;
          margin-bottom: 20px;
        }

        fluent-card {
          padding: 20px;
          background: #fff;
        }

        form {
          display: grid;
          gap: 12px;
        }

        .actions {
          display: flex;
          justify-content: flex-end;
        }

        .error {
          color: #b00020;
          font-size: 13px;
        }

      </style>

      <div class="wrap">
        <div class="brand">🤧 tissues</div>
        <h1 class="title">Sign in</h1>
        <div class="subtitle">Use your Google Cloud Identity Platform account.</div>

        <fluent-card>
          <form id="login-form" autocomplete="on" method="POST" action="${this.basePath()}">
            <input id="next-field" type="hidden" name="next" value="">
            <input id="next-exp-field" type="hidden" name="next_exp" value="">
            <input id="next-sig-field" type="hidden" name="next_sig" value="">
            <input id="email-field" type="hidden" name="email" value="">
            <input id="password-field" type="hidden" name="password" value="">
            <fluent-text-field
              id="email-input"
              type="email"
              appearance="outline"
              placeholder="Email"
              autocomplete="username"
              ${loading ? "disabled" : ""}
            ></fluent-text-field>
            <fluent-text-field
              id="password-input"
              type="password"
              appearance="outline"
              placeholder="Password"
              autocomplete="current-password"
              ${loading ? "disabled" : ""}
            ></fluent-text-field>
            ${error ? `<div class="error">${error}</div>` : ""}
            <div class="actions">
              <fluent-button appearance="accent" type="submit" id="submit-btn" ${loading ? "disabled" : ""}>
                ${loading ? "Signing in..." : "Sign in"}
              </fluent-button>
            </div>
          </form>
        </fluent-card>
      </div>
    `;

    const nextField = this.shadowRoot.getElementById("next-field");
    if (nextField) {
      nextField.value = this.nextParam();
    }
    const nextExpField = this.shadowRoot.getElementById("next-exp-field");
    if (nextExpField) {
      nextExpField.value = this.queryParam("next_exp");
    }
    const nextSigField = this.shadowRoot.getElementById("next-sig-field");
    if (nextSigField) {
      nextSigField.value = this.queryParam("next_sig");
    }

    this.shadowRoot.getElementById("login-form").onsubmit = (e) => {
      e.preventDefault();
      if (!loading) {
        this.submit();
      }
    };
  }
}

if (!customElements.get("tissues-auth-login")) {
  customElements.define("tissues-auth-login", TissuesAuthLogin);
}
