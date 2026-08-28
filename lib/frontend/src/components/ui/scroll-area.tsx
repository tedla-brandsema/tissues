import * as ScrollAreaPrimitive from "@radix-ui/react-scroll-area";
import { cn } from "@tissues/frontend/lib/utils";

export function ScrollArea({ className, children, ...props }: ScrollAreaPrimitive.ScrollAreaProps) {
  return <ScrollAreaPrimitive.Root className={cn("relative overflow-hidden", className)} {...props}><ScrollAreaPrimitive.Viewport className="h-full w-full">{children}</ScrollAreaPrimitive.Viewport><ScrollAreaPrimitive.Scrollbar orientation="vertical" className="flex w-2.5 p-px"><ScrollAreaPrimitive.Thumb className="flex-1 rounded-full bg-border" /></ScrollAreaPrimitive.Scrollbar></ScrollAreaPrimitive.Root>;
}
