import { Link } from "react-router";

export function NotFound() {
  return (
    <div>
      <h1 className="text-2xl font-semibold tracking-tight">Page not found</h1>
      <p className="mt-1 text-sm text-zinc-600">
        Nothing lives at this address.{" "}
        <Link to="/" className="text-accent underline">
          Go to the start page
        </Link>
        .
      </p>
    </div>
  );
}
