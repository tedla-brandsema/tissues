import { Button } from "@tissues/frontend/components/ui/button";
import { Input } from "@tissues/frontend/components/ui/input";
import { type LoginBootstrap, readLoginBootstrap } from "./bootstrap";

export interface AppProps {
  bootstrap?: LoginBootstrap;
}

export function App({ bootstrap = readLoginBootstrap() }: AppProps) {
  return (
    <main className="login-shell">
      <section className="login-panel" aria-labelledby="login-title">
        <p className="brand">🤧 tissues</p>
        <h1 id="login-title">Sign in</h1>
        <p className="login-help">Use your Identity Platform account.</p>
        {bootstrap.invalidCredentials && <p className="login-error" role="alert">Invalid email or password.</p>}
        <form method="post" action={bootstrap.action}>
          <input type="hidden" name="next" value={bootstrap.next} />
          <input type="hidden" name="next_exp" value={bootstrap.nextExp} />
          <input type="hidden" name="next_sig" value={bootstrap.nextSig} />
          <label htmlFor="email">Email</label>
          <Input id="email" name="email" type="email" autoComplete="username" required />
          <label htmlFor="password">Password</label>
          <Input id="password" name="password" type="password" autoComplete="current-password" required />
          <Button type="submit">Sign in</Button>
        </form>
      </section>
    </main>
  );
}
