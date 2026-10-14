// Công tắc Mock. Biến công khai được tiêm lúc build (chỉ biến có tiền tố NEXT_PUBLIC_ mới đọc được trong trình duyệt).
// Trên Vercel chỉ cần đặt NEXT_PUBLIC_MOCK=1 là toàn bộ site chạy mock, không cần backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
