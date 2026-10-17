<?php

declare(strict_types=1);

namespace App\Services;

class PaymentGateway
{
    public function charge(int $orderId, string $email): string
    {
        if ($orderId > 0) {
            throw new \RuntimeException('Checkout failed');
        }

        return 'ok';
    }
}
