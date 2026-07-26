import { Component, type ErrorInfo, type ReactNode } from "react";
import { Link } from "react-router";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { AlertCircle, Home, RefreshCw } from "lucide-react";

interface Props {
  children: ReactNode;
  /** Optional fallback when no onRetry/onGoHome provided */
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

/**
 * Catches React errors in the tree and shows a fallback with Retry and Go home
 * so the app doesn’t go blank.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: null };

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("ErrorBoundary caught:", error, info.componentStack);
  }

  handleRetry = (): void => {
    this.setState({ hasError: false, error: null });
  };

  render(): ReactNode {
    if (!this.state.hasError || !this.state.error) {
      return this.props.children;
    }

    if (this.props.fallback) {
      return this.props.fallback;
    }

    return (
      <div
        className="flex min-h-[40vh] flex-col items-center justify-center gap-4 px-6 py-12 text-center"
        role="alert"
        aria-live="assertive"
      >
        <AlertCircle className="h-12 w-12 text-destructive" aria-hidden />
        <h2 className="text-lg font-semibold">Something went wrong</h2>
        <p className="max-w-md text-sm text-muted-foreground">
          An error occurred. You can try again or go back to the dashboard.
        </p>
        <div className="flex flex-wrap items-center justify-center gap-3">
          <Button onClick={this.handleRetry} className="gap-2">
            <RefreshCw className="h-4 w-4" />
            Retry
          </Button>
          <Link to="/" className={cn(buttonVariants({ variant: "outline" }), "gap-2")}>
            <Home className="h-4 w-4" />
            Go home
          </Link>
        </div>
      </div>
    );
  }
}
