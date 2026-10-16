<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Services\PaymentGateway;

class CheckoutController
{
    public function store(array $input): string
    {
        $gateway = new PaymentGateway();
        $orderId = 1234;

        return $gateway->charge($orderId, $input['email'] ?? '');
    }
}
