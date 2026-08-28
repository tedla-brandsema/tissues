import * as AlertDialogPrimitive from "@radix-ui/react-alert-dialog";
import type { HTMLAttributes } from "react";
import { cn } from "@tissues/frontend/lib/utils";

export const AlertDialog = AlertDialogPrimitive.Root;
export const AlertDialogTrigger = AlertDialogPrimitive.Trigger;
export const AlertDialogCancel = AlertDialogPrimitive.Cancel;
export const AlertDialogAction = AlertDialogPrimitive.Action;

export function AlertDialogContent({ className, children, ...props }: AlertDialogPrimitive.AlertDialogContentProps) {
  return <AlertDialogPrimitive.Portal>
    <AlertDialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40" />
    <AlertDialogPrimitive.Content className={cn("fixed left-1/2 top-1/2 z-50 grid w-[min(32rem,calc(100%-2rem))] -translate-x-1/2 -translate-y-1/2 gap-4 rounded-lg border bg-background p-6 text-foreground shadow-xl", className)} {...props}>
      {children}
    </AlertDialogPrimitive.Content>
  </AlertDialogPrimitive.Portal>;
}
export function AlertDialogHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("space-y-1.5", className)} {...props} />;
}
export function AlertDialogFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex justify-end gap-2", className)} {...props} />;
}
export function AlertDialogTitle(props: AlertDialogPrimitive.AlertDialogTitleProps) {
  return <AlertDialogPrimitive.Title className="text-lg font-semibold" {...props} />;
}
export function AlertDialogDescription(props: AlertDialogPrimitive.AlertDialogDescriptionProps) {
  return <AlertDialogPrimitive.Description className="text-sm text-muted-foreground" {...props} />;
}
