import { Skeleton } from "@/components/ui/skeleton";

export function GallerySkeleton() {
  return <div className="gallery-skeleton" aria-label="正在读取照片" role="status">
    {[0, 1, 2, 3, 4, 5].map(i => <Skeleton key={i} className="aspect-[3/4]" />)}
  </div>;
}
