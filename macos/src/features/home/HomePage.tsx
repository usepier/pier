import { Anchor } from "lucide-react";
import { Empty } from "@/ui/Empty";

export function HomePage() {
  return (
    <Empty
      icon={<Anchor size={28} strokeWidth={1.5} />}
      title="Home"
      detail="Sessions across every repository show up here."
    />
  );
}
